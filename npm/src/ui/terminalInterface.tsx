import React, { useEffect, useMemo, useRef, useState } from 'react';
import { Box, Text, useApp, useInput, useStdin, useStdout } from 'ink';
import Spinner from 'ink-spinner';
import type { MastraEngine, HarnessEvent, ApprovalAction } from '../agent/mastraEngine.js';
import type { AgentMode } from '../types/index.js';
import { deleteAt, deleteBefore, insertAt, layoutEditor, lineEndAt, lineStartAt, moveHorizontal, moveVerticalWrapped, normalizePaste } from './editor.js';
import { renderMarkdown, type MarkdownSegment } from './markdown.js';
import { KITTY_POP, KITTY_PUSH, isEncodedKey, parseRawKey, translateCsiU } from './keys.js';

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

  useEffect(() => {
    const onResize = (): void => setWidth(widthOf(stdout));
    stdout.on('resize', onResize);
    stdout.write(KITTY_PUSH);
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
    return () => { stdout.off('resize', onResize); unsubscribe(); process.off('exit', onUnload); stdout.write(KITTY_POP); };
  }, [engine, stdout]);

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

  useInput((value, key) => {
    const translated = translateCsiU(value);
    const submit = key.return || translated?.kind === 'submit';
    const newline = value === '\n' || translated?.kind === 'newline';
    const escape = key.escape || translated?.kind === 'escape';
    const tab = key.tab || translated?.kind === 'tab';
    const ctrlC = (key.ctrl && value === 'c') || translated?.kind === 'ctrlC';
    const ctrlM = (key.ctrl && value.toLowerCase() === 'm') || translated?.kind === 'ctrlM';
    const ctrlO = (key.ctrl && value.toLowerCase() === 'o') || translated?.kind === 'ctrlO';
    if (approval) {
      const resolve = approvalResolve.current;
      if (value === 'y' || value === 'Y' || submit) { setApproval(null); approvalResolve.current = null; resolve?.(true); }
      else if (value === 'n' || value === 'N' || escape) { setApproval(null); approvalResolve.current = null; resolve?.(false); }
      return;
    }
    if (ctrlC) { exit(); return; }
    if (ctrlM) { cycleMode(); return; }
    if (ctrlO) {
      setPickerOpen(true);
      void loadCombos();
      return;
    }
    if (pickerOpen) {
      if (escape) { setPickerOpen(false); return; }
      if (key.upArrow || value === 'k') { setComboIndex((current) => clamp(current - 1, 0, Math.max(0, combos.length - 1))); return; }
      if (key.downArrow || value === 'j') { setComboIndex((current) => clamp(current + 1, 0, Math.max(0, combos.length - 1))); return; }
      if (submit && combos.length > 0) {
        const selected = combos[comboIndex];
        engine.selectModel(selected);
        setPickerOpen(false);
        setLines((current) => [...current, { role: 'assistant', text: `combo selected: ${selected} (saved as default)` }]);
      }
      return;
    }
    if (tab) { setEdit((current) => insertAt(current.value, current.cursor, '\t')); return; }
    if (submit) {
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
      return;
    }
    if (newline) { setEdit((current) => insertAt(current.value, current.cursor, '\n')); return; }
    if (key.backspace) { setEdit((current) => deleteBefore(current.value, current.cursor)); return; }
    if (key.delete) { setEdit((current) => deleteAt(current.value, current.cursor)); return; }
    if (key.leftArrow) { setEdit((current) => ({ ...current, cursor: moveHorizontal(current.value, current.cursor, -1) })); return; }
    if (key.rightArrow) { setEdit((current) => ({ ...current, cursor: moveHorizontal(current.value, current.cursor, +1) })); return; }
    if (key.upArrow) { setEdit((current) => ({ ...current, cursor: moveVerticalWrapped(current.value, current.cursor, -1, inputWidth) })); return; }
    if (key.downArrow) { setEdit((current) => ({ ...current, cursor: moveVerticalWrapped(current.value, current.cursor, +1, inputWidth) })); return; }
    if (value.length > 1 && !isEncodedKey(value)) { setEdit((current) => insertAt(current.value, current.cursor, normalizePaste(value))); return; }
    if (!key.ctrl && !key.meta && value && !isEncodedKey(value)) setEdit((current) => insertAt(current.value, current.cursor, value));
  });

  useEffect(() => {
    if (!stdin) return;
    const onData = (chunk: Buffer): void => {
      const key = parseRawKey(chunk.toString());
      if (!key) return;
      setEdit((current) => key === 'home'
        ? { ...current, cursor: lineStartAt(current.value, current.cursor) }
        : { ...current, cursor: lineEndAt(current.value, current.cursor) });
    };
    stdin.on('data', onData);
    return () => { stdin.off('data', onData); };
  }, [stdin]);

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
    <Box justifyContent="space-between"><Text dimColor>Enter send · Shift+Enter new line · ←→↑↓ move · Ctrl+O combos · Ctrl+M mode · Ctrl+C quit</Text><Text dimColor>{mode} · {engine.state.activeModel} · compression {compression}</Text></Box>
  </Box>;
}