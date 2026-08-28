import React, { useEffect, useMemo, useRef, useState } from 'react';
import { Box, Text, useApp, useInput, useStdout } from 'ink';
import Spinner from 'ink-spinner';
import type { MastraEngine, HarnessEvent, ApprovalAction } from '../agent/mastraEngine.js';
import type { AgentMode } from '../types/index.js';

interface Props { engine: MastraEngine }

type LineRole = 'user' | 'assistant' | 'error' | 'thinking' | 'tool';
interface Line { role: LineRole; text: string; model?: string; toolName?: string; url?: string }
interface Row { role: LineRole; text: string; label: string; first: boolean }
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

export function TerminalInterface({ engine }: Props): React.ReactElement {
  const { exit } = useApp();
  const { stdout } = useStdout();
  const [width, setWidth] = useState(() => widthOf(stdout));
  const [input, setInput] = useState('');
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
    const onUnload = (): void => engine.stop();
    process.on('exit', onUnload);
    return () => { stdout.off('resize', onResize); unsubscribe(); process.off('exit', onUnload); };
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
    if (approval) {
      const resolve = approvalResolve.current;
      if (value === 'y' || value === 'Y' || key.return) { setApproval(null); approvalResolve.current = null; resolve?.(true); }
      else if (value === 'n' || value === 'N' || key.escape) { setApproval(null); approvalResolve.current = null; resolve?.(false); }
      return;
    }
    if (key.ctrl && value === 'c') { exit(); return; }
    if (key.ctrl && value.toLowerCase() === 'm') { cycleMode(); return; }
    if (key.ctrl && value.toLowerCase() === 'o') {
      setPickerOpen(true);
      void loadCombos();
      return;
    }
    if (pickerOpen) {
      if (key.escape) { setPickerOpen(false); return; }
      if (key.upArrow || value === 'k') { setComboIndex((current) => clamp(current - 1, 0, Math.max(0, combos.length - 1))); return; }
      if (key.downArrow || value === 'j') { setComboIndex((current) => clamp(current + 1, 0, Math.max(0, combos.length - 1))); return; }
      if (key.return && combos.length > 0) {
        const selected = combos[comboIndex];
        engine.selectModel(selected);
        setPickerOpen(false);
        setLines((current) => [...current, { role: 'assistant', text: `combo selected: ${selected} (saved as default)` }]);
      }
      return;
    }
    if (key.return) {
      const prompt = input.trim();
      if (!prompt || busy) return;
      setInput(''); setBusy(true); setError(undefined);
      setLines((current) => [...current, { role: 'user', text: prompt }]);
      void engine.run(prompt).then(() => {
        /* answer is streamed live via text_delta / text events */
      }).catch((reason: unknown) => {
        const message = reason instanceof Error ? reason.message : String(reason);
        setError(message); setLines((current) => [...current, { role: 'error', text: message }]);
      }).finally(() => setBusy(false));
      return;
    }
    if (key.backspace || key.delete) { setInput((current) => current.slice(0, -1)); return; }
    if (!key.ctrl && !key.meta && value) setInput((current) => current + value);
  });

  const metrics = engine.client.snapshotMetrics();
  const compression = metrics.compression.inputTokens > 0 ? `${Math.round((1 - metrics.compression.ratio) * 100)}% ${metrics.compression.strategy.toUpperCase()}` : '—';
  const contentWidth = Math.max(20, width - 8);
  const messageHeight = Math.max(4, (stdout.rows ?? 24) - 9);

  const rows = useMemo<Row[]>(() => {
    const out: Row[] = [];
    for (const line of lines) {
      const label = labelFor(line.role, line.model, line.toolName);
      const wrapped = wrap(line.text, contentWidth);
      wrapped.forEach((text, index) => out.push({ role: line.role, text, label, first: index === 0 }));
    }
    return out.slice(-messageHeight);
  }, [lines, contentWidth, messageHeight]);

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
            <Text color={row.role === 'thinking' ? 'yellow' : row.role === 'tool' ? 'magenta' : undefined}>{row.text}</Text>
          </Box>          : <Text key={index} color={row.role === 'thinking' ? 'yellow' : row.role === 'tool' ? 'magenta' : undefined}>{row.text}</Text>)}
      {liveThink !== '' && <Box flexDirection="column" marginTop={1}><Text color="yellow" bold>think</Text><Text color="yellow">{liveThink}</Text></Box>}
      {liveAnswer !== '' && <Box flexDirection="column" marginTop={1}><Text color="green" bold>harness</Text><Text>{liveAnswer}</Text></Box>}
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
    <Box borderStyle="round" borderColor={error ? 'red' : 'cyan'} paddingX={1} marginTop={1}>
      <Text color="cyan">› </Text><Text>{clip(input || 'type a task and press enter', contentWidth)}</Text>
    </Box>
    <Box justifyContent="space-between"><Text dimColor>Ctrl+O combos · Ctrl+M mode · Ctrl+C quit</Text><Text dimColor>{mode} · {engine.state.activeModel} · compression {compression}</Text></Box>
  </Box>;
}