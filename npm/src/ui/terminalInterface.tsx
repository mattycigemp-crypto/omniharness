import React, { useEffect, useMemo, useRef, useState } from 'react';
import { Box, Text, useApp, useInput, useStdin, useStdout } from 'ink';
import Spinner from 'ink-spinner';
import type { MastraEngine, HarnessEvent, ApprovalAction } from '../agent/mastraEngine.js';
import type { AgentMode } from '../types/index.js';
import { deleteAt, deleteBefore, insertAt, layoutEditor, lineEndAt, lineStartAt, moveHorizontal, moveVerticalWrapped, normalizePaste } from './editor.js';
import { renderMarkdown, type MarkdownSegment } from './markdown.js';
import { KITTY_POP, KITTY_PUSH, KITTY_QUERY, isEncodedKey, isKittyQueryResponse, parseRawKey, type KeyAction } from './keys.js';

interface Props { engine: MastraEngine }

type LineRole = 'user' | 'assistant' | 'error' | 'thinking' | 'tool';
interface Line { role: LineRole; text: string; model?: string; toolName?: string; url?: string }
interface Row { role: LineRole; segments: readonly MarkdownSegment[]; label: string; first: boolean }
interface LiveIndices { think: number | null; answer: number | null }

const MODE_SEQ: AgentMode[] = ['plan', 'build', 'research'];

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

function labelFor(role: LineRole, model?: string, toolName?: string): string {
  switch (role) {
    case 'user': return 'you';
    case 'error': return 'error';
    case 'thinking': return 'think';
    case 'tool': return toolName ? `tool · ${toolName}` : 'tool';
    default: return model ?? 'harness';
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
  const [lines, setLines] = useState<Line[]>([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>();
  const [pickerOpen, setPickerOpen] = useState(false);
  const [combos, setCombos] = useState<readonly string[]>([]);
  const [comboIndex, setComboIndex] = useState(0);
  const [comboError, setComboError] = useState<string>();
  const [mode, setMode] = useState<AgentMode>(engine.state.mode);
  const liveRef = useRef<LiveIndices>({ think: null, answer: null });
  const [approval, setApproval] = useState<ApprovalAction | null>(null);
  const approvalResolve = useRef<((ok: boolean) => void) | null>(null);

  const [liveThink, setLiveThink] = useState('');
  const [liveAnswer, setLiveAnswer] = useState('');
  const [kitty, setKitty] = useState<boolean | null>(null);

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
          setLines((current) => [...current, { role: 'assistant', text: event.content }]);
          setLiveAnswer('');
          break;
        case 'tool_start':
          setLines((current) => [...current, { role: 'tool', text: event.tool, toolName: `${event.tool} →` }]);
          break;
        case 'tool_result':
          setLines((current) => [...current, { role: 'tool', text: `  ${event.summary}`, toolName: 'result' }]);
          break;
        case 'preview':
          setLines((current) => [...current, { role: 'tool', text: `preview: ${event.url}`, toolName: 'preview', url: event.url }]);
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

  const loadCombos = async (): Promise<void> => {
    setComboError(undefined);
    try {
      const accountCombos = await engine.client.listCombos();
      const names = accountCombos.map((combo) => combo.name).filter((name) => name.trim() !== '');
      setCombos(names);
      setComboIndex(0);
    } catch (reason: unknown) {
      setCombos([]);
      setComboError(reason instanceof Error ? reason.message : String(reason));
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

  /** Apply a semantic key action, honoring the active overlay (approval, picker). */
  const applyAction = (action: KeyAction): void => {
    if (approval && action.kind !== 'submit' && action.kind !== 'escape') return;
    if (pickerOpen && action.kind !== 'submit' && action.kind !== 'escape' && action.kind !== 'up' && action.kind !== 'down') return;
    switch (action.kind) {
      case 'submit':
        if (approval) { approve(true); return; }
        if (pickerOpen) {
          const selected = combos[comboIndex];
          if (selected) {
            engine.selectModel(selected);
            setPickerOpen(false);
            setLines((current) => [...current, { role: 'assistant', text: `combo selected: ${selected} (saved as default)` }]);
          }
          return;
        }
        {
          const prompt = edit.value.trim();
          if (!prompt || busy) return;
          setEdit({ value: '', cursor: 0 }); setBusy(true); setError(undefined);
          setLines((current) => [...current, { role: 'user', text: prompt }]);
          void engine.run(prompt).then(() => {
            /* answer is streamed live via text_delta / text events */
          }).catch((reason: unknown) => {
            const message = reason instanceof Error ? reason.message : String(reason);
            setError(message); setLines((current) => [...current, { role: 'error', text: message }]);
          }).finally(() => setBusy(false));
        }
        return;
      case 'escape':
        if (approval) { approve(false); return; }
        if (pickerOpen) { setPickerOpen(false); return; }
        return;
      case 'ctrlC': exit(); return;
      case 'ctrlM': cycleMode(); return;
      case 'ctrlO': setPickerOpen(true); void loadCombos(); return;
      case 'up':
        if (pickerOpen) { setComboIndex((current) => clamp(current - 1, 0, Math.max(0, combos.length - 1))); return; }
        setEdit((current) => ({ ...current, cursor: moveVerticalWrapped(current.value, current.cursor, -1, inputWidth) }));
        return;
      case 'down':
        if (pickerOpen) { setComboIndex((current) => clamp(current + 1, 0, Math.max(0, combos.length - 1))); return; }
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

  const metrics = engine.client.snapshotMetrics();
  const compression = metrics.compression.inputTokens > 0 ? `${Math.round((1 - metrics.compression.ratio) * 100)}% ${metrics.compression.strategy.toUpperCase()}` : '—';
  const contentWidth = Math.max(20, width - 8);
  const messageHeight = Math.max(4, (stdout.rows ?? 24) - 9);

  const rows = useMemo<Row[]>(() => {
    const out: Row[] = [];
    for (const line of lines) {
      const label = labelFor(line.role, line.model, line.toolName);
      const markdown = line.role !== 'tool' && line.role !== 'error';
      const wrapped: MarkdownSegment[][] = markdown
        ? renderMarkdown(line.text, contentWidth)
        : wrap(line.text, contentWidth).map((text) => [{ text }]);
      wrapped.forEach((segments, index) => out.push({ role: line.role, segments, label, first: index === 0 }));
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
      {lines.length === 0 && <Box flexDirection="column" marginTop={2}><Text color="cyan" bold>Ready when you are.</Text><Text dimColor>Describe the work. OmniHarness routes it through your OmniRoute account. Ctrl+M cycles plan · build · research.</Text></Box>}
      {rows.map((row, index) => row.first
        ? <Box key={index} flexDirection="column" marginTop={index === 0 ? 0 : 1}>
            <Text color={colorFor(row.role)} bold>{row.label}</Text>
            <SegmentText segments={row.segments} role={row.role} />
          </Box>          : <SegmentText key={index} segments={row.segments} role={row.role} />)}
      {liveThink !== '' && <Box flexDirection="column" marginTop={1}><Text color="yellow" bold>think</Text>{liveThinkLines.map((segments, index) => <SegmentText key={index} segments={segments} role="thinking" />)}</Box>}
      {liveAnswer !== '' && <Box flexDirection="column" marginTop={1}><Text color="green" bold>harness</Text>{liveAnswerLines.map((segments, index) => <SegmentText key={index} segments={segments} role="assistant" />)}</Box>}
      {engine.state.preview && <Text color="green" dimColor>preview live · {engine.state.preview.url}</Text>}
      {busy && <Text color="cyan"><Spinner type="dots" /> working in {mode} mode on {engine.state.activeModel}</Text>}
    </Box>
    {pickerOpen && <Box flexDirection="column" borderStyle="round" borderColor="magenta" paddingX={2} paddingY={1}>
      <Text bold color="magenta">Choose an OmniRoute combo</Text>
      <Text dimColor>↑↓ / j k navigate · enter select · esc close</Text>
      {comboError && <Text color="red">{clip(comboError, contentWidth)}</Text>}
      {combos.length === 0 && !comboError && <Text dimColor>No account combos returned by OmniRoute.</Text>}
      {combos.map((combo, index) => <Text key={combo} color={index === comboIndex ? 'cyan' : undefined}>{index === comboIndex ? '› ' : '  '}{combo}{combo === engine.state.activeModel ? '  ✓' : ''}</Text>)}
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
    <Box justifyContent="space-between"><Text dimColor>Enter send · Shift+Enter / Ctrl+J new line · ←→↑↓ move · Ctrl+O combos · Ctrl+M mode · Ctrl+C quit</Text><Text dimColor>{mode} · {engine.state.activeModel} · compression {compression}</Text></Box>
  </Box>;
}