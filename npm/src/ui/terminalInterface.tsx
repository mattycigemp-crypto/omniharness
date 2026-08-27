import React, { useEffect, useMemo, useState } from 'react';
import { Box, Text, useApp, useInput, useStdout } from 'ink';
import Spinner from 'ink-spinner';
import type { MastraEngine } from '../agent/mastraEngine.js';

interface Props { engine: MastraEngine }
interface Line { role: 'user' | 'assistant' | 'error'; text: string }

function widthOf(stdout: NodeJS.WriteStream): number {
  return Math.max(48, stdout.columns ?? 80);
}

function clip(text: string, width: number): string {
  return text.length <= width ? text : `${text.slice(0, Math.max(0, width - 1))}…`;
}

export function TerminalInterface({ engine }: Props): React.ReactElement {
  const { exit } = useApp();
  const { stdout } = useStdout();
  const [width, setWidth] = useState(() => widthOf(stdout));
  const [input, setInput] = useState('');
  const [lines, setLines] = useState<Line[]>([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | undefined>();

  useEffect(() => {
    const onResize = (): void => setWidth(widthOf(stdout));
    stdout.on('resize', onResize);
    return () => { stdout.off('resize', onResize); };
  }, [stdout]);

  useInput((value, key) => {
    if (key.ctrl && value === 'c') { exit(); return; }
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
    if (key.backspace) { setInput((current) => current.slice(0, -1)); return; }
    if (!key.ctrl && !key.meta && value) setInput((current) => current + value);
  });

  const contentWidth = Math.max(20, width - 6);
  const metrics = engine.client.snapshotMetrics();
  const compression = metrics.compression.inputTokens > 0 ? `${Math.round((1 - metrics.compression.ratio) * 100)}% via ${metrics.compression.strategy.toUpperCase()}` : 'pending';
  const visibleLines = useMemo(() => lines.slice(-Math.max(3, (stdout.rows ?? 24) - 9)), [lines, stdout.rows]);

  return <Box flexDirection="column" width={width} paddingX={1}>
    <Box borderStyle="round" borderColor="cyan" paddingX={1} justifyContent="space-between">
      <Text bold color="cyan">OmniHarness v2.0</Text>
      <Text dimColor>OmniRoute Local :20128</Text>
    </Box>
    <Box marginTop={1} flexDirection="row">
      <Box flexDirection="column" width={Math.max(24, Math.floor(width * 0.27))} borderStyle="single" borderColor="gray" paddingX={1}>
        <Text bold>ROUTE</Text><Text>provider: {metrics.fallback.activeProvider ?? 'auto'}</Text>
        <Text>compression: {compression}</Text><Text>cooldown: {metrics.fallback.cooldownUntil ?? 'none'}</Text>
        <Text>requests: {metrics.requestCount}</Text>
      </Box>
      <Box flexDirection="column" flexGrow={1} marginLeft={1} borderStyle="single" borderColor="gray" paddingX={1}>
        {visibleLines.length === 0 && <Text dimColor>Describe a task to begin.</Text>}
        {visibleLines.map((line, index) => <Text key={`${index}-${line.role}`} color={line.role === 'user' ? 'blue' : line.role === 'error' ? 'red' : undefined}>
          {line.role === 'user' ? 'YOU  ' : line.role === 'error' ? 'ERR  ' : 'AGENT'} {clip(line.text, contentWidth)}
        </Text>)}
        {busy && <Text color="cyan"><Spinner type="dots" /> working through OmniRoute…</Text>}
      </Box>
    </Box>
    <Box marginTop={1} borderStyle="round" borderColor={error ? 'red' : 'cyan'} paddingX={1}>
      <Text color="cyan">› </Text><Text>{clip(input || 'type a task and press enter', contentWidth)}</Text>
    </Box>
    <Text dimColor>Ctrl+C quit · model {engine.state.activeModel}</Text>
  </Box>;
}
