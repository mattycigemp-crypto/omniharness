import React, { useEffect, useMemo, useRef, useState } from 'react';
import { Box, Text, useApp, useInput, useStdin, useStdout } from 'ink';
import Spinner from 'ink-spinner';
import type { MastraEngine, HarnessEvent, ApprovalAction } from '../agent/mastraEngine.js';
import type { AgentMode, HarnessMessage, TodoItem } from '../types/index.js';
import { appendPromptHistory, loadPromptHistory } from '../promptHistory.js';
import { deleteAt, deleteBefore, insertAt, layoutEditor, lineEndAt, lineStartAt, moveHorizontal, moveVerticalWrapped, normalizePaste } from './editor.js';
import { renderMarkdown, type MarkdownSegment } from './markdown.js';
import { KITTY_POP, KITTY_PUSH, KITTY_QUERY, isEncodedKey, isKittyQueryResponse, parseRawKey, type KeyAction } from './keys.js';

interface Props { engine: MastraEngine }

type LineRole = 'user' | 'assistant' | 'error' | 'thinking' | 'tool';
interface Line { role: LineRole; text: string; model?: string; toolName?: string; url?: string; saved?: string }

/** Map a restored transcript message into a rendered line (used on startup). */
function lineFromMessage(message: HarnessMessage): Line {
  switch (message.role) {
    case 'user': return { role: 'user', text: message.content };
    case 'assistant': return { role: 'assistant', text: message.content, model: message.model };
    case 'thought': return { role: 'thinking', text: message.content };
    case 'tool': return { role: 'tool', text: message.content, toolName: message.toolName ?? 'tool' };
    case 'error': return { role: 'error', text: message.content };
    default: return { role: 'tool', text: message.content, toolName: message.role }; // action / command
  }
}
interface Row { role: LineRole; segments: readonly MarkdownSegment[]; label: string; first: boolean; saved?: string }
interface LiveIndices { think: number | null; answer: number | null }
interface PickerItem { id: string; group: 'combos' | 'auto'; strategy?: string }

const MODE_SEQ: AgentMode[] = ['plan', 'build', 'research', 'crazy'];

const clamp = (value: number, min: number, max: number): number => Math.min(max, Math.max(min, value));
const widthOf = (stdout: NodeJS.WriteStream): number => Math.max(48, stdout.columns ?? 80);
const clip = (text: string, width: number): string => text.length <= width ? text : `${text.slice(0, Math.max(0, width - 1))}…`;

/** Word-wrap text to width, honoring existing newlines and hard-breaking long words. */
function wrap(text: string, width: number): string[] {
  const out: string[] = [];
  for (const paragraph of text.split('\n')) {
    if (paragraph === '') { out.push(''); continue; }
    let current = '';
    for (const word of paragraph.split(' ')) {
      const candidate = current ? `${current} ${word}` : word;
      if (candidate.length <= width) { current = candidate; continue; }
      if (current) { out.push(current); current = ''; }
      let rest = word;
      while (rest.length > width) { out.push(rest.slice(0, width)); rest = rest.slice(width); }
      current = rest;
    }
    if (current) out.push(current);
  }
  return out.length > 0 ? out : [''];
}

function labelFor(role: LineRole, model?: string, toolName?: string, fallback?: string): string {
  switch (role) {
    case 'user': return 'you';
    case 'error': return 'error';
    case 'thinking': return 'think';
    case 'tool': return toolName ? `tool · ${toolName}` : 'tool';
    default: return model ?? fallback ?? 'assistant';
  }
}

function colorFor(role: LineRole): string {
  switch (role) {
    case 'user': return 'blue';
    case 'error': return 'red';
    case 'thinking': return 'yellow';
    case 'tool': return 'magenta';
    default: return 'green';
  }
}

function SegmentText({ segments, role }: { segments: readonly MarkdownSegment[]; role: LineRole }): React.ReactElement {
  const base = role === 'thinking' ? 'yellow' : role === 'tool' ? 'magenta' : undefined;
  return <Text color={base}>{segments.map((segment, index) => (
    <Text key={index} bold={segment.bold} italic={segment.italic} strikethrough={segment.strikethrough} underline={segment.underline} dimColor={segment.dim} color={segment.color}>{segment.text}</Text>
  ))}</Text>;
}

export function TerminalInterface({ engine }: Props): React.ReactElement {
  const { exit } = useApp();
  const { stdout } = useStdout();
  const { stdin } = useStdin();
  const [width, setWidth] = useState(() => widthOf(stdout));
  const [edit, setEdit] = useState({ value: '', cursor: 0 });
  const inputWidth = Math.max(16, width - 12);
  // Replay a resumed transcript (hydrated by the engine) into the chat area.
  const [lines, setLines] = useState<Line[]>(() => engine.state.messages.map(lineFromMessage));
  const promptHistoryRef = useRef<string[]>([]);
  const [promptHistory, setPromptHistory] = useState<string[]>([]);
  const historyIdxRef = useRef(-1);
  const syncPromptHistory = (next: string[]): void => { promptHistoryRef.current = next; setPromptHistory(next); };
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>();
  const [pickerOpen, setPickerOpen] = useState(false);
  const [pickerItems, setPickerItems] = useState<readonly PickerItem[]>([]);
  const [pickerIndex, setPickerIndex] = useState(0);
  const [pickerError, setPickerError] = useState<string>();
  const [mode, setMode] = useState<AgentMode>(engine.state.mode);
  const liveRef = useRef<LiveIndices>({ think: null, answer: null });
  const [approval, setApproval] = useState<ApprovalAction | null>(null);
  const approvalResolve = useRef<((ok: boolean) => void) | null>(null);

  const [liveThink, setLiveThink] = useState('');
  const [liveAnswer, setLiveAnswer] = useState('');
  const [kitty, setKitty] = useState<boolean | null>(null);
  const [taskQueue, setTaskQueue] = useState<readonly TodoItem[]>(engine.state.taskQueue);
  const [currentTool, setCurrentTool] = useState<string>();

  useEffect(() => {
    let alive = true;
    // Seed history from disk, but only if the user hasn't already submitted a
    // prompt this session (handles the async load racing a submit).
    void loadPromptHistory().then((history) => {
      if (alive && promptHistoryRef.current.length === 0) syncPromptHistory(history);
    }).catch(() => { /* history is best-effort */ });
    return () => { alive = false; };
  }, []);

  useEffect(() => {
    const onResize = (): void => setWidth(widthOf(stdout));
    stdout.on('resize', onResize);
    stdout.write(KITTY_PUSH);
    let kittyTimer: ReturnType<typeof setTimeout> | undefined;
    // Terminals with kitty support answer the query (ESC[? flags u); others ignore it.
    const onKitty = (chunk: Buffer): void => {
      if (!isKittyQueryResponse(chunk.toString())) return;
      if (kittyTimer) clearTimeout(kittyTimer);
      stdin?.off('data', onKitty);
      setKitty(true);
    };
    if (stdin) {
      stdin.on('data', onKitty);
      kittyTimer = setTimeout(() => { setKitty(false); stdin.off('data', onKitty); }, 300);
      stdout.write(KITTY_QUERY);
    } else {
      setKitty(false);
    }
    const unsubscribe = engine.subscribe((event: HarnessEvent) => {
      switch (event.type) {
        case 'thinking_delta': setLiveThink((current) => current + event.delta); break;
        case 'text_delta': setLiveAnswer((current) => current + event.delta); break;
        case 'thinking':
          if (event.text) setLines((current) => [...current, { role: 'thinking', text: event.text }]);
          setLiveThink('');
          break;
        case 'text':
          setLines((current) => [...current, {
            role: 'assistant', text: event.content, model: event.model,
            saved: event.compression ? `${Math.round((1 - event.compression.ratio) * 100)}% saved (${event.compression.strategy.toUpperCase()}) · ${event.compression.savedTokens.toLocaleString()} tokens` : undefined,
          }]);
          setLiveAnswer('');
          break;
        case 'tool_start':
          setLines((current) => [...current, { role: 'tool', text: event.tool, toolName: `${event.tool} →` }]);
          setCurrentTool(event.tool);
          break;
        case 'tool_result':
          setLines((current) => [...current, { role: 'tool', text: `  ${event.summary}`, toolName: 'result' }]);
          break;
        case 'todos':
          setTaskQueue(event.todos);
          break;
        case 'preview':
          setLines((current) => [...current, { role: 'tool', text: `preview: ${event.url}`, toolName: 'preview', url: event.url }]);
          break;
        case 'attach':
          setLines((current) => [...current, { role: 'tool', text: `attached: ${event.name} (${event.kind}, ${event.size} bytes)`, toolName: 'attach' }]);
          break;
      }
    });
    engine.setApprovalHandler((action) => new Promise<boolean>((resolve) => {
      approvalResolve.current = resolve;
      setApproval(action);
    }));
    const onUnload = (): void => { engine.stop(); stdout.write(KITTY_POP); };
    process.on('exit', onUnload);
    return () => {
      if (kittyTimer) clearTimeout(kittyTimer);
      stdin?.off('data', onKitty);
      stdout.off('resize', onResize);
      unsubscribe();
      process.off('exit', onUnload);
      stdout.write(KITTY_POP);
    };
  }, [engine, stdout, stdin]);

  const loadPicker = async (): Promise<void> => {
    setPickerError(undefined);
    try {
      const [accountCombos, modelIds] = await Promise.all([engine.client.listCombos(), engine.client.listModels()]);
      const items: PickerItem[] = [];
      for (const combo of accountCombos) {
        if (combo.name.trim() !== '' && !items.some((item) => item.id === combo.name)) {
          items.push({ id: combo.name, group: 'combos', strategy: combo.strategy });
        }
      }
      for (const id of [...new Set(modelIds.filter((id) => id.startsWith('auto/')))].sort()) {
        if (!items.some((item) => item.id === id)) items.push({ id, group: 'auto' });
      }
      setPickerItems(items);
      setPickerIndex(Math.max(0, items.findIndex((item) => item.id === engine.state.activeModel)));
    } catch (reason: unknown) {
      setPickerItems([]);
      setPickerError(reason instanceof Error ? reason.message : String(reason));
    }
  };

  const cycleMode = (): void => {
    const next = MODE_SEQ[(MODE_SEQ.indexOf(mode) + 1) % MODE_SEQ.length];
    setMode(next);
    engine.state.mode = next;
    setLines((current) => [...current, { role: 'tool', text: `mode → ${next}`, toolName: 'mode' }]);
  };

  const approve = (ok: boolean): void => {
    const resolve = approvalResolve.current;
    setApproval(null); approvalResolve.current = null;
    resolve?.(ok);
  };

  /** ↑ walks older prompts, ↓ walks newer; ↓ past the newest clears the input. */
  const navigateHistory = (older: boolean): void => {
    const history = promptHistoryRef.current;
    if (history.length === 0) return;
    const idx = historyIdxRef.current;
    const next = older
      ? (idx < 0 ? 0 : Math.min(idx + 1, history.length - 1))
      : (idx <= 0 ? -1 : idx - 1);
    historyIdxRef.current = next;
    setEdit(next < 0 ? { value: '', cursor: 0 } : { value: history[next] ?? '', cursor: (history[next] ?? '').length });
  };

  /** Whether ↑/↓ should browse prompt history instead of moving the text caret. */
  const browsingHistory = (): boolean => historyIdxRef.current >= 0 || edit.value === '';

  /** Apply a semantic key action, honoring the active overlay (approval, picker). */
  const applyAction = (action: KeyAction): void => {
    if (approval && action.kind !== 'submit' && action.kind !== 'escape' && action.kind !== 'ctrlC') return;
    if (pickerOpen && action.kind !== 'submit' && action.kind !== 'escape' && action.kind !== 'up' && action.kind !== 'down') return;
    switch (action.kind) {
      case 'submit':
        if (approval) { approve(true); return; }
        if (pickerOpen) {
          const selected = pickerItems[pickerIndex];
          if (selected) {
            void engine.selectModel(selected.id);
            setPickerOpen(false);
            setLines((current) => [...current, { role: 'assistant', text: `model selected: ${selected.id} (saved as default)` }]);
          }
          return;
        }
        {
          const raw = edit.value.trim();
          const attachMatch = /^\/attach\s+(.+)$/.exec(raw);
          if (raw === '/help') {
            setLines((current) => [...current,
              { role: 'tool', text: '/help — show commands', toolName: 'commands' },
              { role: 'tool', text: '/clear — start a fresh conversation', toolName: 'commands' },
              { role: 'tool', text: '/attach <files> — attach files to the next message', toolName: 'commands' },
              { role: 'tool', text: 'keys: Ctrl+O models · Ctrl+E mode · Ctrl+C cancel · ↑/↓ prompt history', toolName: 'commands' },
            ]);
            setEdit({ value: '', cursor: 0 }); historyIdxRef.current = -1;
            return;
          }
          if (raw === '/clear') {
            historyIdxRef.current = -1;
            syncPromptHistory([]);
            setLines([]);
            void engine.clearHistory().catch(() => { /* /clear resets local state; persistence is best-effort */ });
            setEdit({ value: '', cursor: 0 });
            return;
          }
          const prompt = attachMatch ? '' : raw;
          if (busy || (!prompt && !attachMatch)) return;
          setEdit({ value: '', cursor: 0 }); setBusy(true); setError(undefined);
          historyIdxRef.current = -1;
          if (prompt) {
            syncPromptHistory([prompt, ...promptHistoryRef.current.filter((entry) => entry !== prompt)].slice(0, 200));
            void appendPromptHistory(prompt).catch(() => { /* best-effort */ });
          }
          if (!attachMatch) setLines((current) => [...current, { role: 'user', text: prompt }]);
          void (async (): Promise<void> => {
            try {
              if (attachMatch) await engine.attach(attachMatch[1]!.split(/\s+/).filter(Boolean));
              await engine.run(prompt);
              /* answer is streamed live via text_delta / text events */
            } catch (reason: unknown) {
              const message = reason instanceof Error ? reason.message : String(reason);
              setError(message); setLines((current) => [...current, { role: 'error', text: message }]);
            } finally {
              setBusy(false);
            }
          })();
        }
        return;
      case 'escape':
        if (approval) { approve(false); return; }
        if (pickerOpen) { setPickerOpen(false); return; }
        return;
      case 'ctrlC':
        // While a run is in flight, Ctrl+C cancels that run; when idle it quits.
        if (busy) { engine.cancel(); return; }
        exit();
        return;
      case 'ctrlM': cycleMode(); return;
      case 'ctrlO': setPickerOpen(true); void loadPicker(); return;
      case 'ctrlE': cycleMode(); return;
      case 'up':
        if (pickerOpen) { setPickerIndex((current) => clamp(current - 1, 0, Math.max(0, pickerItems.length - 1))); return; }
        if (!busy && promptHistoryRef.current.length > 0 && browsingHistory()) { navigateHistory(true); return; }
        setEdit((current) => ({ ...current, cursor: moveVerticalWrapped(current.value, current.cursor, -1, inputWidth) }));
        return;
      case 'down':
        if (pickerOpen) { setPickerIndex((current) => clamp(current + 1, 0, Math.max(0, pickerItems.length - 1))); return; }
        if (!busy && browsingHistory()) { navigateHistory(false); return; }
        setEdit((current) => ({ ...current, cursor: moveVerticalWrapped(current.value, current.cursor, +1, inputWidth) }));
        return;
      case 'left': setEdit((current) => ({ ...current, cursor: moveHorizontal(current.value, current.cursor, -1) })); return;
      case 'right': setEdit((current) => ({ ...current, cursor: moveHorizontal(current.value, current.cursor, +1) })); return;
      case 'home': setEdit((current) => ({ ...current, cursor: lineStartAt(current.value, current.cursor) })); return;
      case 'end': setEdit((current) => ({ ...current, cursor: lineEndAt(current.value, current.cursor) })); return;
      case 'newline': setEdit((current) => insertAt(current.value, current.cursor, '\n')); return;
      case 'backspace': setEdit((current) => deleteBefore(current.value, current.cursor)); return;
      case 'delete': setEdit((current) => deleteAt(current.value, current.cursor)); return;
      case 'tab': setEdit((current) => insertAt(current.value, current.cursor, '\t')); return;
    }
  };

  // Legacy keys Ink parses correctly (\r, \n, \x08, ESC[A arrows, ctrl+letters) plus text and paste.
  useInput((value, key) => {
    if (approval) {
      if (value === 'y' || value === 'Y' || key.return) applyAction({ kind: 'submit' });
      else if (value === 'n' || value === 'N' || key.escape) applyAction({ kind: 'escape' });
      return;
    }
    if (key.ctrl && value === 'c') { applyAction({ kind: 'ctrlC' }); return; }
    if (key.ctrl && value.toLowerCase() === 'm') { applyAction({ kind: 'ctrlM' }); return; }
    if (key.ctrl && value.toLowerCase() === 'o') { applyAction({ kind: 'ctrlO' }); return; }
    if (key.ctrl && value.toLowerCase() === 'e') { applyAction({ kind: 'ctrlE' }); return; }
    if (pickerOpen) {
      if (key.escape) { applyAction({ kind: 'escape' }); return; }
      if (key.upArrow || value === 'k') { applyAction({ kind: 'up' }); return; }
      if (key.downArrow || value === 'j') { applyAction({ kind: 'down' }); return; }
      if (key.return) { applyAction({ kind: 'submit' }); return; }
      return;
    }
    if (key.tab) { applyAction({ kind: 'tab' }); return; }
    if (key.return) { applyAction({ kind: 'submit' }); return; }
    if (value === '\n') { applyAction({ kind: 'newline' }); return; }
    if (key.backspace) { applyAction({ kind: 'backspace' }); return; }
    if (key.escape) { applyAction({ kind: 'escape' }); return; }
    if (!key.upArrow && !key.downArrow && !key.leftArrow && !key.rightArrow) historyIdxRef.current = -1; // typing exits history recall
    if (key.upArrow) { applyAction({ kind: 'up' }); return; }
    if (key.downArrow) { applyAction({ kind: 'down' }); return; }
    if (key.leftArrow) { applyAction({ kind: 'left' }); return; }
    if (key.rightArrow) { applyAction({ kind: 'right' }); return; }
    // 0x7f is Backspace on Windows ConPTY and the kitty-encoded keys are owned by the raw stdin listener.
    if (value.length > 1 && !isEncodedKey(value)) { setEdit((current) => insertAt(current.value, current.cursor, normalizePaste(value))); return; }
    if (!key.ctrl && !key.meta && value && !isEncodedKey(value)) setEdit((current) => insertAt(current.value, current.cursor, value));
  });

  // Raw stdin: disambiguate Windows Backspace (0x7f), the real Delete key, and kitty-protocol keys.
  useEffect(() => {
    if (!stdin) return;
    const onData = (chunk: Buffer): void => {
      const action = parseRawKey(chunk.toString());
      if (action) applyAction(action);
    };
    stdin.on('data', onData);
    return () => { stdin.off('data', onData); };
  }, [stdin, applyAction]);

  // Legacy Ctrl+M is the CR byte — identical to Enter — so mode cycling needs a
  // distinguishable key (Ctrl+E works everywhere); kitty terminals also keep Ctrl+M.
  const modeKey = kitty === true ? 'M' : 'E';

  const metrics = engine.client.snapshotMetrics();
  const compression = metrics.compression.inputTokens > 0 ? `${Math.round((1 - metrics.compression.ratio) * 100)}% ${metrics.compression.strategy.toUpperCase()}` : '—';
  const hud = [mode, engine.state.activeModel];
  if (metrics.fallback.activeProvider) hud.push(metrics.fallback.activeProvider);
  if (metrics.remainingQuota !== undefined) hud.push(`quota ${metrics.remainingQuota}`);
  if (metrics.fallback.attempts > 0) hud.push(`fb ${metrics.fallback.attempts}`);
  hud.push(`saved ${compression}`);
  const contentWidth = Math.max(20, width - 8);
  const messageHeight = Math.max(4, (stdout.rows ?? 24) - 9);

  const rows = useMemo<Row[]>(() => {
    const out: Row[] = [];
    for (const line of lines) {
      const label = labelFor(line.role, line.model, line.toolName, engine.state.activeModel);
      const markdown = line.role !== 'tool' && line.role !== 'error';
      const wrapped: MarkdownSegment[][] = markdown
        ? renderMarkdown(line.text, contentWidth)
        : wrap(line.text, contentWidth).map((text) => [{ text }]);
      wrapped.forEach((segments, index) => out.push({ role: line.role, segments, label, first: index === 0 }));
      // A dim one-line savings note right under the assistant's answer.
      if (line.saved) out.push({ role: 'assistant', segments: [{ text: line.saved }], label: '', first: false, saved: line.saved });
    }
    return out.slice(-messageHeight);
  }, [lines, contentWidth, messageHeight]);

  const liveThinkLines = useMemo(() => renderMarkdown(liveThink, contentWidth), [liveThink, contentWidth]);
  const liveAnswerLines = useMemo(() => renderMarkdown(liveAnswer, contentWidth), [liveAnswer, contentWidth]);

  return <Box flexDirection="column" width={width} height={stdout.rows ?? 24} paddingX={2}>
    <Box justifyContent="space-between" paddingY={1}>
      <Text bold color="cyan">OMNIHARNESS <Text dimColor>· {mode} mode</Text></Text>
      <Text dimColor>OMNIROUTE :20128</Text>
    </Box>
    <Box flexDirection="column" flexGrow={1}>
      {lines.length === 0 && <Box flexDirection="column" marginTop={2}><Text color="cyan" bold>Ready when you are.</Text><Text dimColor>Describe the work. OmniHarness routes it through your OmniRoute account. Ctrl+{modeKey} cycles plan · build · research · crazy.</Text></Box>}
      {rows.map((row, index) => row.saved
        ? row.saved !== '' ? <Text key={index} dimColor>{row.saved}</Text> : null
        : row.first
          ? <Box key={index} flexDirection="column" marginTop={index === 0 ? 0 : 1}>
              <Text color={colorFor(row.role)} bold>{row.label}</Text>
              <SegmentText segments={row.segments} role={row.role} />
            </Box>
          : <SegmentText key={index} segments={row.segments} role={row.role} />)}
      {liveThink !== '' && <Box flexDirection="column" marginTop={1}><Text color="yellow" bold>think</Text>{liveThinkLines.map((segments, index) => <SegmentText key={index} segments={segments} role="thinking" />)}</Box>}
      {liveAnswer !== '' && <Box flexDirection="column" marginTop={1}><Text color="green" bold>{engine.state.activeModel}</Text>{liveAnswerLines.map((segments, index) => <SegmentText key={index} segments={segments} role="assistant" />)}</Box>}
      {engine.state.preview && <Text color="green" dimColor>preview live · {engine.state.preview.url}</Text>}
      {busy && <Text color="cyan"><Spinner type="dots" /> working in {mode} mode on {engine.state.activeModel}{currentTool ? ` · now ${currentTool}` : ''}</Text>}
    </Box>
    {taskQueue.length > 0 && <Box flexDirection="column" borderStyle="round" borderColor="cyan" paddingX={2} paddingY={1} marginTop={1}>
      <Box justifyContent="space-between"><Text bold color="cyan">plan</Text><Text dimColor>{taskQueue.filter((item) => item.status === 'done').length}/{taskQueue.length} done</Text></Box>
      {taskQueue.slice(-6).map((item) => {
        const marker = item.status === 'done' ? '✓' : item.status === 'active' ? '▸' : '○';
        const color = item.status === 'done' ? 'green' : item.status === 'active' ? 'cyan' : undefined;
        return <Text key={item.id} color={color} dimColor={item.status === 'done'}>{marker} {clip(item.title, contentWidth - 4)}</Text>;
      })}
    </Box>}
    {pickerOpen && <Box flexDirection="column" borderStyle="round" borderColor="magenta" paddingX={2} paddingY={1}>
      <Text bold color="magenta">Choose an OmniRoute model</Text>
      <Text dimColor>↑↓ / j k navigate · enter select · esc close</Text>
      {pickerError && <Text color="red">{clip(pickerError, contentWidth)}</Text>}
      {pickerItems.length === 0 && !pickerError && <Text dimColor>No models returned by OmniRoute.</Text>}
      {pickerItems.map((item, index) => {
        const header = index === 0 || pickerItems[index - 1]!.group !== item.group
          ? <Text key={`h-${item.group}`} dimColor bold>{item.group === 'combos' ? 'your combos' : 'auto engine'}</Text>
          : null;
        return <Box key={item.id} flexDirection="column">{header}
          <Text color={index === pickerIndex ? 'cyan' : undefined}>{index === pickerIndex ? '› ' : '  '}{item.id}{item.strategy ? <Text dimColor>  · {item.strategy}</Text> : null}{item.id === engine.state.activeModel ? '  ✓' : ''}</Text>
        </Box>;
      })}
    </Box>}
    {approval && <Box flexDirection="column" borderStyle="round" borderColor="yellow" paddingX={2} paddingY={1} marginTop={1}>
      <Text bold color="yellow">Approve {approval.tool}?</Text>
      <Text dimColor>args: {clip(typeof approval.input === 'string' ? approval.input : JSON.stringify(approval.input), contentWidth)}</Text>
      <Text dimColor>y approve · n deny</Text>
    </Box>}
    <Box borderStyle="round" borderColor={error ? 'red' : 'cyan'} paddingX={1} marginTop={1} flexDirection="column">
      {edit.value === ''
        ? <Text color="cyan">› <Text dimColor>type a task and press enter</Text></Text>
        : layoutEditor(edit.value, edit.cursor, inputWidth).lines.map((text, index) => <Text key={index} color="cyan">{index === 0 ? '› ' : '  '}{text}</Text>)}
    </Box>
    {kitty !== null && <Text dimColor>{kitty ? 'kitty protocol active — Shift+Enter makes a new line' : 'this terminal can\'t distinguish Shift+Enter from Enter — use Ctrl+J for a new line'}</Text>}
    <Box justifyContent="space-between"><Text dimColor>Enter send · Shift+Enter/Ctrl+J new line · Ctrl+O models · Ctrl+{modeKey} mode · ↑/↓ recall prompts · Ctrl+C cancel/quit · /help</Text><Text dimColor>{hud.join(' · ')}</Text></Box>
  </Box>;
}