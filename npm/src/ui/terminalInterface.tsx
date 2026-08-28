import React, { useEffect, useMemo, useState } from 'react';
import { Box, Text, useApp, useInput, useStdout } from 'ink';
import Spinner from 'ink-spinner';
import type { MastraEngine } from '../agent/mastraEngine.js';

interface Props { engine: MastraEngine }
interface Line { role: 'user' | 'assistant' | 'error'; text: string }

const clamp = (value: number, min: number, max: number): number => Math.min(max, Math.max(min, value));
const widthOf = (stdout: NodeJS.WriteStream): number => Math.max(48, stdout.columns ?? 80);
const clip = (text: string, width: number): string => text.length <= width ? text : `${text.slice(0, Math.max(0, width - 1))}…`;

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

  useEffect(() => {
    const onResize = (): void => setWidth(widthOf(stdout));
    stdout.on('resize', onResize);
    return () => { stdout.off('resize', onResize); };
  }, [stdout]);

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

  useInput((value, key) => {
    if (key.ctrl && value === 'c') { exit(); return; }
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
        engine.state.activeModel = selected;
        setPickerOpen(false);
        setLines((current) => [...current, { role: 'assistant', text: `combo selected: ${selected}` }]);
      }
      return;
    }
    if (key.return) {
      const prompt = input.trim();
      if (!prompt || busy) return;
      setInput(''); setBusy(true); setError(undefined);
      setLines((current) => [...current, { role: 'user', text: prompt }]);
      void engine.run(prompt).then((answer) => {
        setLines((current) => [...current, { role: 'assistant', text: answer }]);
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
  const visibleLines = useMemo(() => lines.slice(-Math.max(4, (stdout.rows ?? 24) - 8)), [lines, stdout.rows]);

  return <Box flexDirection="column" width={width} height={stdout.rows ?? 24} paddingX={2}>
    <Box justifyContent="space-between" paddingY={1}>
      <Text bold color="cyan">OMNIHARNESS <Text dimColor>· local cockpit</Text></Text>
      <Text dimColor>OMNIROUTE :20128</Text>
    </Box>
    <Box flexDirection="column" flexGrow={1}>
      {visibleLines.length === 0 && <Box flexDirection="column" marginTop={2}><Text color="cyan" bold>Ready when you are.</Text><Text dimColor>Describe the work. OmniHarness will route it through your OmniRoute account.</Text></Box>}
      {visibleLines.map((line, index) => <Box key={`${index}-${line.role}`} flexDirection="column" marginBottom={1}>
        <Text color={line.role === 'user' ? 'blue' : line.role === 'error' ? 'red' : 'green'} bold>{line.role === 'user' ? 'you' : line.role === 'error' ? 'error' : 'harness'}</Text>
        <Text>{clip(line.text, contentWidth)}</Text>
      </Box>)}
      {busy && <Text color="cyan"><Spinner type="dots" /> working through {engine.state.activeModel}</Text>}
    </Box>
    {pickerOpen && <Box flexDirection="column" borderStyle="round" borderColor="magenta" paddingX={2} paddingY={1}>
      <Text bold color="magenta">Choose an OmniRoute combo</Text>
      <Text dimColor>↑↓ / j k navigate · enter select · esc close</Text>
      {comboError && <Text color="red">{clip(comboError, contentWidth)}</Text>}
      {combos.length === 0 && !comboError && <Text dimColor>No account combos returned by OmniRoute.</Text>}
      {combos.map((combo, index) => <Text key={combo} color={index === comboIndex ? 'cyan' : undefined}>{index === comboIndex ? '› ' : '  '}{combo}{combo === engine.state.activeModel ? '  ✓' : ''}</Text>)}
    </Box>}
    <Box borderStyle="round" borderColor={error ? 'red' : 'cyan'} paddingX={1} marginTop={1}>
      <Text color="cyan">› </Text><Text>{clip(input || 'type a task and press enter', contentWidth)}</Text>
    </Box>
    <Box justifyContent="space-between"><Text dimColor>Ctrl+O combos · Ctrl+C quit</Text><Text dimColor>model {engine.state.activeModel} · compression {compression}</Text></Box>
  </Box>;
}
