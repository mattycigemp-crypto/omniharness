import React, { useEffect, useMemo, useRef, useState } from 'react';
import { Box, Static, Text, useApp, useInput, useStdin, useStdout } from 'ink';
import Spinner from 'ink-spinner';
import type { MastraEngine, HarnessEvent, ApprovalAction, ApprovalResolution } from '../agent/mastraEngine.js';
import type { AgentMode, HarnessMessage, PermissionMode, TodoItem } from '../types/index.js';
import { appendPromptHistory, loadPromptHistory } from '../promptHistory.js';
import { listSessions, loadSnapshot, saveSnapshot, deleteSnapshot } from '../sessionList.js';
import { deleteAt, deleteBefore, insertAt, layoutEditor, lineEndAt, lineStartAt, moveHorizontal, moveVerticalWrapped, normalizePaste } from './editor.js';
import { renderMarkdown, type MarkdownSegment } from './markdown.js';
import { looksLikeDiff, diffSegments } from './diff.js';
import { palette, type Palette } from './palette.js';
import { contextMeter, meterBar } from './modelWindows.js';
import { BEL, SYNC_QUERY, isSyncOutputReply, osc9Notify, osc52Copy, shouldNudgeOnFinish, wrapSynchronizedOutput } from './termcaps.js';
import { KITTY_POP, KITTY_PUSH, KITTY_QUERY, isEncodedKey, isKittyQueryResponse, parseRawKey, type KeyAction } from './keys.js';

interface Props { engine: MastraEngine }

type LineRole = 'user' | 'assistant' | 'error' | 'thinking' | 'tool';
interface Line { role: LineRole; text: string; model?: string; toolName?: string; url?: string; saved?: string; provider?: string; fallback?: boolean }

/** A tool call collapsed into a compact card (name, target, status). */
interface ToolCard {
  id: string;
  name: string;
  target: string;
  status: 'running' | 'done' | 'error';
  summary?: string;
  /** Raw output trail captured before/while running (rendered as a diff if it looks like one). */
  trail?: string;
}

/** One parallel worker in a CRAZY-mode swarm. */
interface AgentLane { id: string; label: string; status: 'spawned' | 'working' | 'done' | 'error'; note?: string }

interface PickerItem { id: string; group: 'combos' | 'auto'; strategy?: string }

const PALETTE = palette();

/** Border / accent colour per working mode — the input frame changes with it. */
const MODE_ACCENT: Record<AgentMode, string> = {
  plan: PALETTE.info,
  build: PALETTE.success,
  research: PALETTE.accent,
  crazy: PALETTE.error,
};

/** Distinct hues for swarm agents — identity, not severity. */
const AGENT_COLORS = [PALETTE.accent, PALETTE.info, PALETTE.warn, PALETTE.success, 'magenta', PALETTE.error] as const;

function describeTarget(name: string, input: unknown): string {
  if (input && typeof input === 'object') {
    const record = input as Record<string, unknown>;
    if (typeof record.path === 'string') return record.path;
    if (typeof record.command === 'string') return record.command;
    if (typeof record.query === 'string') return record.query.slice(0, 48);
  }
  return '';
}

/** Short verb shown on a tool card header, per tool type. */
function toolVerb(tool: string): string {
  switch (tool) {
    case 'read_file': return 'read';
    case 'write_file': return 'edit';
    case 'run_command': return '$';
    case 'git_diff': return 'diff';
    case 'semantic_search': return 'search';
    case 'index_workspace': return 'index';
    case 'update_todo': return 'plan';
    case 'write_memory': return 'memory';
    case 'start_preview': return 'preview';
    case 'route': return 'route';
    default: return tool;
  }
}

function phaseFor(tool: string): string {
  switch (tool) {
    case 'read_file': return 'reading files';
    case 'write_file': return 'editing files';
    case 'run_command': return 'running commands';
    case 'semantic_search': return 'searching the workspace';
    case 'index_workspace': return 'indexing the workspace';
    case 'git_diff': return 'checking the diff';
    case 'update_todo': return 'updating the plan';
    case 'start_preview': return 'starting preview';
    case 'write_memory': return 'remembering';
    default: return tool;
  }
}

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

const MODE_SEQ: AgentMode[] = ['plan', 'build', 'research', 'crazy'];

const PERM_SEQ: PermissionMode[] = ['ask', 'acceptEdits', 'bypass'];
const PERM_LABEL: Record<PermissionMode, string> = { ask: 'manual', acceptEdits: 'accept edits', bypass: 'bypass' };
const PERM_GLYPH: Record<PermissionMode, string> = { ask: '○', acceptEdits: '◐', bypass: '●' };
const PERM_COLOR = (p: PermissionMode): string => (p === 'bypass' ? PALETTE.error : p === 'acceptEdits' ? PALETTE.warn : PALETTE.muted);

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
    case 'thinking': return 'thinking';
    case 'tool': return toolName ? `tool · ${toolName}` : 'tool';
    default: return model ?? fallback ?? 'assistant';
  }
}

function colorFor(role: LineRole, p: Palette = PALETTE): string {
  switch (role) {
    case 'user': return p.info;
    case 'error': return p.error;
    case 'thinking': return p.warn;
    case 'tool': return p.muted;
    default: return p.accent;
  }
}

function SegmentText({ segments, role }: { segments: readonly MarkdownSegment[]; role: LineRole }): React.ReactElement {
  const base = role === 'thinking' ? PALETTE.warn : role === 'tool' ? PALETTE.muted : role === 'assistant' ? undefined : role === 'user' ? PALETTE.info : undefined;
  return <Text color={base}>{segments.map((segment, index) => (
    <Text key={index} bold={segment.bold} italic={segment.italic} strikethrough={segment.strikethrough} underline={segment.underline} dimColor={segment.dim} color={segment.color ?? base}>{segment.text}</Text>
  ))}</Text>;
}

/** Expanded body of a tool card, rendered per tool type. */
function renderToolBody(card: ToolCard, width: number, p: Palette): React.ReactElement {
  const trail = card.trail;
  if (!trail) return card.summary ? <Text dimColor>{card.summary.slice(0, width)}</Text> : <Text dimColor>(no output)</Text>;
  if (looksLikeDiff(trail)) {
    return <>{diffSegments(trail, width).slice(0, 14).map((segments, index) => <SegmentText key={index} segments={segments} role="tool" />)}</>;
  }
  const rows = trail.split('\n').slice(0, 14);
  if (card.name === 'write_file') {
    return <>{rows.map((line, index) => <Text key={index} color={p.success}>{line.slice(0, width)}</Text>)}</>;
  }
  if (card.name === 'run_command') {
    return <>{rows.map((line, index) => {
      const exit = /^exit (\d+)/.exec(line);
      const color = exit ? (exit[1] === '0' ? p.success : p.error) : undefined;
      return <Text key={index} color={color} dimColor={color === undefined}>{line.slice(0, width)}</Text>;
    })}</>;
  }
  return <>{rows.map((line, index) => <Text key={index} dimColor>{line.slice(0, width)}</Text>)}</>;
}

/**
 * One settled transcript entry, rendered exactly once into `<Static>` (native
 * scrollback). Label row + wrapped body; tool/error lines render literally,
 * everything else as markdown.
 */
function TranscriptEntry({ line, width, fallbackModel }: { line: Line; width: number; fallbackModel: string }): React.ReactElement {
  const baseLabel = labelFor(line.role, line.model, line.toolName, fallbackModel);
  const label = line.role === 'assistant' && line.provider
    ? `${baseLabel}  ·  via ${line.provider}${line.fallback ? ' (failover)' : ''}`
    : baseLabel;
  const asMarkdown = line.role !== 'tool' && line.role !== 'error';
  const rows: MarkdownSegment[][] = asMarkdown
    ? renderMarkdown(line.text, width)
    : wrap(line.text, width).map((text) => [{ text }]);
  const bullet = line.role === 'user' ? '❯' : line.role === 'assistant' ? '◆' : line.role === 'thinking' ? '·' : line.role === 'error' ? '✕' : '⋯';
  return <Box flexDirection="column" marginTop={1}>
    <Text bold color={colorFor(line.role)}>{bullet} {label}</Text>
    {rows.map((segments, index) => <SegmentText key={index} segments={segments} role={line.role} />)}
    {line.saved ? <Text dimColor>  {line.saved}</Text> : null}
  </Box>;
}

/** Branded splash shown until the first prompt. */
function Hero({ width, endpoint, model, mode, perm }: { width: number; endpoint: string; model: string; mode: AgentMode; perm: PermissionMode }): React.ReactElement {
  return <Box flexDirection="column" borderStyle="round" borderColor={PALETTE.accent} paddingX={2} paddingY={1} marginBottom={1} width={Math.min(width, 76)}>
    <Text bold color={PALETTE.accent}>◇ OMNIHARNESS</Text>
    <Text dimColor>the OmniRoute-native agent harness</Text>
    <Box marginTop={1} flexDirection="column">
      <Text><Text dimColor>gateway  </Text>{endpoint}</Text>
      <Text><Text dimColor>model    </Text>{model}</Text>
      <Text><Text dimColor>mode     </Text><Text color={MODE_ACCENT[mode]}>{mode}</Text><Text dimColor>  ·  Ctrl+E cycles plan · build · research · crazy</Text></Text>
      <Text><Text dimColor>perms    </Text><Text color={mode === 'crazy' ? PALETTE.error : PERM_COLOR(perm)}>{mode === 'crazy' ? 'bypass' : PERM_LABEL[perm]}</Text><Text dimColor>  ·  Shift+Tab cycles manual · accept edits · bypass</Text></Text>
    </Box>
    <Box marginTop={1}><Text dimColor>describe the work and press enter · /help for commands</Text></Box>
  </Box>;
}

export function TerminalInterface({ engine }: Props): React.ReactElement {
  const { exit } = useApp();
  const { stdout } = useStdout();
  const { stdin } = useStdin();
  const [width, setWidth] = useState(() => widthOf(stdout));
  const [edit, setEdit] = useState({ value: '', cursor: 0 });
  const inputWidth = Math.max(16, width - 12);
  // Settled transcript. Rendered once each into <Static> — the terminal's own
  // scrollback is the history; there is no in-app viewport to scroll.
  const [lines, setLines] = useState<Line[]>(() => engine.state.messages.map(lineFromMessage));
  const [staticKey, setStaticKey] = useState(0); // bumped on /clear to reset <Static>
  const promptHistoryRef = useRef<string[]>([]);
  const historyIdxRef = useRef(-1);
  const syncPromptHistory = (next: string[]): void => { promptHistoryRef.current = next; };
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>();
  const [pickerOpen, setPickerOpen] = useState(false);
  const [pickerItems, setPickerItems] = useState<readonly PickerItem[]>([]);
  const [pickerIndex, setPickerIndex] = useState(0);
  const [pickerError, setPickerError] = useState<string>();
  const [mode, setMode] = useState<AgentMode>(engine.state.mode);
  const [permMode, setPermMode] = useState<PermissionMode>(engine.state.permissionMode ?? 'ask');
  const [approval, setApproval] = useState<ApprovalAction | null>(null);
  const approvalResolve = useRef<((decision: ApprovalResolution) => void) | null>(null);

  const [liveThink, setLiveThink] = useState('');
  const [liveAnswer, setLiveAnswer] = useState('');
  const [kitty, setKitty] = useState<boolean | null>(null);
  const [sessionsList, setSessionsList] = useState<{ name: string; savedAt: string }[]>([]);
  const [sessionsOpen, setSessionsOpen] = useState(false);
  const [sessionsIndex, setSessionsIndex] = useState(0);
  const [taskQueue, setTaskQueue] = useState<readonly TodoItem[]>(engine.state.taskQueue);
  const [currentTool, setCurrentTool] = useState<string>();
  const [toolCards, setToolCards] = useState<readonly ToolCard[]>([]);
  const [expandedTool, setExpandedTool] = useState<string>();
  const [agents, setAgents] = useState<readonly AgentLane[]>([]);
  const runStartedAt = useRef<number | null>(null);
  const [now, setNow] = useState(() => Date.now());
  const queuedRef = useRef<{ prompt: string; attachSpec?: string } | null>(null);
  const [queued, setQueued] = useState<string>();
  const [layoutDebug, setLayoutDebug] = useState(false);
  const syncRestoreRef = useRef<(() => void) | null>(null);

  const pushLine = (line: Line): void => setLines((current) => [...current, line]);
  const pushTool = (text: string, toolName = 'system'): void => pushLine({ role: 'tool', text, toolName });

  useEffect(() => {
    let alive = true;
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
    // One probe pass: kitty keyboard protocol + synchronized output (DECSET 2026).
    const onProbe = (chunk: Buffer): void => {
      const text = chunk.toString();
      if (isSyncOutputReply(text) && syncRestoreRef.current === null) {
        syncRestoreRef.current = wrapSynchronizedOutput(stdout as unknown as { write: (c: unknown, ...r: unknown[]) => boolean });
      }
      if (!isKittyQueryResponse(text)) return;
      if (kittyTimer) clearTimeout(kittyTimer);
      stdin?.off('data', onProbe);
      setKitty(true);
    };
    if (stdin) {
      stdin.on('data', onProbe);
      kittyTimer = setTimeout(() => { setKitty(false); stdin.off('data', onProbe); }, 300);
      stdout.write(SYNC_QUERY);
      stdout.write(KITTY_QUERY);
    } else {
      setKitty(false);
    }
    const unsubscribe = engine.subscribe((event: HarnessEvent) => {
      switch (event.type) {
        case 'thinking_delta': setLiveThink((current) => current + event.delta); break;
        case 'text_delta': setLiveAnswer((current) => current + event.delta); break;
        case 'thinking':
          if (event.text) pushLine({ role: 'thinking', text: event.text });
          setLiveThink('');
          break;
        case 'text':
          pushLine({
            role: 'assistant', text: event.content, model: event.model,
            provider: event.provider, fallback: event.fallback,
            saved: event.compression ? `${Math.round((1 - event.compression.ratio) * 100)}% saved (${event.compression.strategy.toUpperCase()}) · ${event.compression.savedTokens.toLocaleString()} tokens` : undefined,
          });
          setLiveAnswer('');
          break;
        case 'route':
          pushLine({
            role: 'tool', toolName: 'route',
            text: event.fallback
              ? `route · failover → ${event.provider ?? 'unknown'} (attempt ${event.attempts + 1})${event.reason ? ` · ${event.reason}` : ''}`
              : `route · ${event.provider ?? 'unknown'}`,
          });
          break;
        case 'agent':
          setAgents((current) => {
            const prev = current.find((lane) => lane.id === event.id);
            const next = current.filter((lane) => lane.id !== event.id);
            // Keep the last meaningful note when a status-only update carries none.
            next.push({ id: event.id, label: event.label, status: event.status, note: event.note ?? prev?.note });
            return next.sort((a, b) => a.id.localeCompare(b.id));
          });
          break;
        case 'tool_start':
          setToolCards((current) => [...current, {
            id: `tc-${Date.now().toString(36)}-${current.length}`, name: event.tool,
            target: describeTarget(event.tool, event.input), status: 'running',
            trail: typeof event.input === 'object' && event.input !== null
              && typeof (event.input as Record<string, unknown>).content === 'string'
              ? String((event.input as Record<string, unknown>).content) : undefined,
          }]);
          setCurrentTool(event.tool);
          break;
        case 'tool_result':
          setToolCards((current) => current.length === 0
            ? current
            : current.map((card, index) => index === current.length - 1
              ? { ...card, status: 'done', summary: event.summary, trail: card.trail ?? event.detail }
              : card));
          setCurrentTool(undefined);
          break;
        case 'todos':
          setTaskQueue(event.todos);
          break;
        case 'preview':
          pushLine({ role: 'tool', text: `preview: ${event.url}`, toolName: 'preview', url: event.url });
          break;
        case 'attach':
          pushLine({ role: 'tool', text: `attached: ${event.name} (${event.kind}, ${event.size} bytes)`, toolName: 'attach' });
          break;
      }
    });
    engine.setApprovalHandler((action) => new Promise<ApprovalResolution>((resolve) => {
      approvalResolve.current = resolve;
      setApproval(action);
    }));
    const onUnload = (): void => { engine.stop(); stdout.write(KITTY_POP); };
    process.on('exit', onUnload);
    return () => {
      if (kittyTimer) clearTimeout(kittyTimer);
      stdin?.off('data', onProbe);
      stdout.off('resize', onResize);
      const pendingApproval = approvalResolve.current;
      approvalResolve.current = null;
      pendingApproval?.({ approved: false });
      engine.stop();
      unsubscribe();
      process.off('exit', onUnload);
      syncRestoreRef.current?.();
      syncRestoreRef.current = null;
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
    pushTool(`mode → ${next}`, 'mode');
  };

  /** Shift+Tab cycles how tool approvals are handled (manual → accept edits → bypass). */
  const cyclePerm = (): void => {
    const next = PERM_SEQ[(PERM_SEQ.indexOf(permMode) + 1) % PERM_SEQ.length]!;
    setPermMode(next);
    engine.state.permissionMode = next;
    pushTool(`permissions → ${PERM_LABEL[next]}${mode === 'crazy' ? ' (crazy mode still bypasses)' : ''}`, 'permissions');
  };

  const approve = (approved: boolean, trust?: string): void => {
    const resolve = approvalResolve.current;
    setApproval(null); approvalResolve.current = null;
    resolve?.({ approved, trust });
  };

  /** Copy the most recent assistant reply to the clipboard via OSC 52. */
  const yankLastBlock = (): void => {
    const target = [...lines].reverse().find((entry) => entry.role === 'assistant') ?? lines[lines.length - 1];
    if (!target) return;
    const seq = osc52Copy(target.text);
    if (!seq) { pushTool('clipboard: block too large to copy', 'clipboard'); return; }
    try {
      stdout.write(seq);
      pushTool(`copied ${target.text.length} chars to clipboard`, 'clipboard');
    } catch { /* clipboard write is best-effort */ }
  };

  /** Kick off an engine run for `prompt`, attaching `attachSpec` files first when given. */
  const startRun = (prompt: string, attachSpec?: string): void => {
    setEdit({ value: '', cursor: 0 }); setBusy(true); setError(undefined);
    setToolCards([]);
    setExpandedTool(undefined);
    setAgents([]);
    if (runStartedAt.current === null) runStartedAt.current = Date.now();
    setNow(Date.now());
    historyIdxRef.current = -1;
    if (prompt) {
      syncPromptHistory([prompt, ...promptHistoryRef.current.filter((entry) => entry !== prompt)].slice(0, 200));
      void appendPromptHistory(prompt).catch(() => { /* best-effort */ });
    }
    if (!attachSpec) pushLine({ role: 'user', text: prompt });
    void (async (): Promise<void> => {
      try {
        if (attachSpec) await engine.attach(attachSpec.split(/\s+/).filter(Boolean));
        await engine.run(prompt);
        // CRAZY mode: once a plan exists, fan the rest of it out across parallel workers.
        if (engine.state.mode === 'crazy' && typeof engine.runSwarm === 'function') {
          const pending = engine.state.taskQueue.filter((item) => item.status === 'pending').length;
          if (pending >= 2) {
            setToolCards([]); // the planning turn's cards give way to the swarm rail
            setExpandedTool(undefined);
            await engine.runSwarm({ maxAgents: 3 });
          }
        }
      } catch (reason: unknown) {
        const message = reason instanceof Error ? reason.message : String(reason);
        setError(message); pushLine({ role: 'error', text: message });
      } finally {
        const startedAt = runStartedAt.current;
        setBusy(false);
        setCurrentTool(undefined);
        setToolCards((current) => current.map((card) => card.status === 'running' ? { ...card, status: 'error' } : card));
        setAgents((current) => current.map((lane) => lane.status === 'working' || lane.status === 'spawned' ? { ...lane, status: 'done' } : lane));
        runStartedAt.current = null;
        setLiveThink('');
        setLiveAnswer('');
        if (startedAt !== null && shouldNudgeOnFinish(Date.now() - startedAt)) {
          try { stdout.write(osc9Notify('OmniHarness — run finished')); stdout.write(BEL); } catch { /* nudge is best-effort */ }
        }
      }
    })();
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

  const browsingHistory = (): boolean => historyIdxRef.current >= 0 || edit.value === '';

  /** Chapters: one per user turn, titled by the prompt's first line. */
  const chapters = (): { index: number; title: string }[] =>
    lines.flatMap((line, index) => line.role === 'user'
      ? [{ index, title: line.text.split('\n')[0]!.slice(0, 60) || '(empty prompt)' }]
      : []);

  const HELP: readonly string[] = [
    '/help — show commands',
    '/clear — start a fresh conversation',
    '/sessions — list saved sessions (enter to resume)',
    '/save <name> — snapshot the current session',
    '/forget <name> — delete a saved session',
    '/attach <files> — attach files to the next message',
    '/find <text> — list transcript lines containing <text>',
    '/chapters — list the turns in this session',
    'keys: Ctrl+O models · Ctrl+E mode · Shift+Tab permissions (manual · accept edits · bypass) · Ctrl+T tool card · Ctrl+Y copy reply · Ctrl+L layout · Ctrl+C cancel/quit · ↑/↓ history',
    'history scrolls in your terminal · a prompt typed mid-run is queued and sent when the run ends',
  ];

  /** Apply a semantic key action, honoring the active overlay (approval, picker). */
  const applyAction = (action: KeyAction): void => {
    if (approval && action.kind !== 'submit' && action.kind !== 'escape' && action.kind !== 'ctrlC') return;
    if (sessionsOpen && action.kind !== 'submit' && action.kind !== 'escape' && action.kind !== 'up' && action.kind !== 'down') return;
    if (pickerOpen && action.kind !== 'submit' && action.kind !== 'escape' && action.kind !== 'up' && action.kind !== 'down') return;
    switch (action.kind) {
      case 'submit':
        if (approval) { approve(true); return; }
        if (sessionsOpen) {
          const selected = sessionsList[sessionsIndex];
          if (selected) {
            void loadSnapshot(engine.state.workspace.root, selected.name).then((snapshot) => {
              if (snapshot == null) { pushLine({ role: 'error', text: `snapshot ${selected.name} is unreadable` }); return; }
              setStaticKey((k) => k + 1);
              setLines([...snapshot.messages.map(lineFromMessage), { role: 'tool', toolName: 'sessions', text: `session resumed: ${selected.name} (${snapshot.messages.length} messages)` }]);
              setTaskQueue(snapshot.taskQueue);
            });
          }
          setSessionsOpen(false);
          return;
        }
        if (pickerOpen) {
          const selected = pickerItems[pickerIndex];
          if (selected) {
            void engine.selectModel(selected.id);
            setPickerOpen(false);
            pushTool(`model → ${selected.id} (saved as default)`, 'model');
          }
          return;
        }
        {
          const raw = edit.value.trim();
          const attachMatch = /^\/attach\s+(.+)$/.exec(raw);
          if (raw === '/help') {
            HELP.forEach((text) => pushTool(text, 'help'));
            setEdit({ value: '', cursor: 0 }); historyIdxRef.current = -1;
            return;
          }
          const findMatch = /^\/find\s+(.+)$/.exec(raw);
          if (findMatch) {
            const needle = findMatch[1]!.toLowerCase();
            const hits = lines.filter((line) => line.text.toLowerCase().includes(needle));
            if (hits.length === 0) pushTool(`no match for "${findMatch[1]}"`, 'find');
            else {
              pushTool(`find "${findMatch[1]}" · ${hits.length} match${hits.length === 1 ? '' : 'es'}`, 'find');
              hits.slice(-6).forEach((hit) => pushTool(`  ${labelFor(hit.role, hit.model, hit.toolName)} · ${clip(hit.text.replace(/\n/g, ' '), Math.max(20, width - 16))}`, 'find'));
            }
            setEdit({ value: '', cursor: 0 }); historyIdxRef.current = -1;
            return;
          }
          if (raw === '/chapters' || raw === '/chapter') {
            const list = chapters();
            if (list.length === 0) pushTool('no chapters yet — each prompt starts one', 'chapters');
            else list.forEach((chapter, order) => pushTool(`${order + 1}. ${chapter.title}`, 'chapters'));
            setEdit({ value: '', cursor: 0 }); historyIdxRef.current = -1;
            return;
          }
          if (raw === '/sessions') {
            void listSessions(engine.state.workspace.root).then((sessions) => {
              setSessionsList(sessions);
              setSessionsIndex(0);
              setSessionsOpen(sessions.length > 0);
              if (sessions.length === 0) pushTool('no saved sessions — use /save <name> to snapshot this one', 'sessions');
            });
            setEdit({ value: '', cursor: 0 });
            return;
          }
          const saveMatch = /^\/save\s+([\w.-]+)$/.exec(raw);
          if (saveMatch) {
            const name = saveMatch[1]!;
            void saveSnapshot(engine.state.workspace.root, name, {
              messages: [...engine.state.messages], taskQueue: [...engine.state.taskQueue], savedAt: new Date().toISOString(),
            }).then(() => pushTool(`session saved: ${name}`, 'sessions'))
              .catch((reason: unknown) => pushLine({ role: 'error', text: `save failed: ${reason instanceof Error ? reason.message : String(reason)}` }));
            setEdit({ value: '', cursor: 0 });
            return;
          }
          const delMatch = /^\/forget\s+([\w.-]+)$/.exec(raw);
          if (delMatch) {
            void deleteSnapshot(engine.state.workspace.root, delMatch[1]!).then(() => pushTool(`session deleted: ${delMatch[1]}`, 'sessions'));
            setEdit({ value: '', cursor: 0 });
            return;
          }
          if (raw === '/clear') {
            historyIdxRef.current = -1;
            setTaskQueue([]);
            setError(undefined);
            setCurrentTool(undefined);
            setToolCards([]);
            setExpandedTool(undefined);
            setAgents([]);
            runStartedAt.current = null;
            setLiveThink('');
            setLiveAnswer('');
            syncPromptHistory([]);
            setStaticKey((k) => k + 1);
            setLines([]);
            void engine.clearHistory().catch(() => { /* best-effort */ });
            setEdit({ value: '', cursor: 0 });
            return;
          }
          const prompt = attachMatch ? '' : raw;
          const attachSpec = attachMatch ? attachMatch[1]! : undefined;
          if (!prompt && !attachSpec) return;
          if (busy) {
            queuedRef.current = { prompt, attachSpec };
            setQueued(prompt || `/attach ${attachSpec ?? ''}`.trim());
            setEdit({ value: '', cursor: 0 });
            historyIdxRef.current = -1;
            return;
          }
          startRun(prompt, attachSpec);
        }
        return;
      case 'escape':
        if (approval) { approve(false); return; }
        if (sessionsOpen) { setSessionsOpen(false); return; }
        if (pickerOpen) { setPickerOpen(false); return; }
        return;
      case 'ctrlC':
        if (busy) { engine.cancel(); return; }
        exit();
        return;
      case 'ctrlM': cycleMode(); return;
      case 'shiftTab': cyclePerm(); return;
      case 'ctrlO': setPickerOpen(true); void loadPicker(); return;
      case 'ctrlE': cycleMode(); return;
      case 'up':
        if (sessionsOpen) { setSessionsIndex((current) => clamp(current - 1, 0, Math.max(0, sessionsList.length - 1))); return; }
        if (pickerOpen) { setPickerIndex((current) => clamp(current - 1, 0, Math.max(0, pickerItems.length - 1))); return; }
        if (!busy && promptHistoryRef.current.length > 0 && browsingHistory()) { navigateHistory(true); return; }
        setEdit((current) => ({ ...current, cursor: moveVerticalWrapped(current.value, current.cursor, -1, inputWidth) }));
        return;
      case 'down':
        if (sessionsOpen) { setSessionsIndex((current) => clamp(current + 1, 0, Math.max(0, sessionsList.length - 1))); return; }
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

  useInput((value, key) => {
    if (approval) {
      if (value === 'y' || value === 'Y' || key.return) { approve(true); return; }
      if (value === 'n' || value === 'N' || key.escape) { approve(false); return; }
      if (value === 't' || value === 'T') { approve(true, approval.scopes[0]?.id); return; }
      const digit = Number(value);
      if (Number.isInteger(digit) && digit >= 1 && digit <= approval.scopes.length) { approve(true, approval.scopes[digit - 1]?.id); return; }
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
    if (sessionsOpen) {
      if (key.escape) { applyAction({ kind: 'escape' }); return; }
      if (key.upArrow) { applyAction({ kind: 'up' }); return; }
      if (key.downArrow) { applyAction({ kind: 'down' }); return; }
      if (key.return) { applyAction({ kind: 'submit' }); return; }
      return;
    }
    if (key.ctrl && value.toLowerCase() === 'l') { setLayoutDebug((current) => !current); return; }
    if (key.ctrl && value.toLowerCase() === 'y') { yankLastBlock(); return; }
    if (key.ctrl && value.toLowerCase() === 't') {
      const latest = toolCards[toolCards.length - 1];
      setExpandedTool((current) => (latest && current === latest.id) ? undefined : (latest ? latest.id : undefined));
      return;
    }
    // Shift+Tab is owned by the raw-stdin listener (parseRawKey handles ESC[Z and
    // kitty ESC[9;2u); swallow it here so it never lands as a literal tab.
    if (key.tab && key.shift) return;
    if (key.tab) { applyAction({ kind: 'tab' }); return; }
    if (key.return) { applyAction({ kind: 'submit' }); return; }
    if (value === '\n') { applyAction({ kind: 'newline' }); return; }
    if (key.backspace) { applyAction({ kind: 'backspace' }); return; }
    if (key.escape) { applyAction({ kind: 'escape' }); return; }
    if (!key.upArrow && !key.downArrow && !key.leftArrow && !key.rightArrow) historyIdxRef.current = -1;
    if (key.upArrow) { applyAction({ kind: 'up' }); return; }
    if (key.downArrow) { applyAction({ kind: 'down' }); return; }
    if (key.leftArrow) { applyAction({ kind: 'left' }); return; }
    if (key.rightArrow) { applyAction({ kind: 'right' }); return; }
    if (value.length > 1 && !isEncodedKey(value)) { setEdit((current) => insertAt(current.value, current.cursor, normalizePaste(value))); return; }
    if (!key.ctrl && !key.meta && value && !isEncodedKey(value)) setEdit((current) => insertAt(current.value, current.cursor, value));
  });

  useEffect(() => {
    if (!stdin) return;
    const onData = (chunk: Buffer): void => {
      const action = parseRawKey(chunk.toString());
      if (action) applyAction(action);
    };
    stdin.on('data', onData);
    return () => { stdin.off('data', onData); };
  }, [stdin, applyAction]);

  useEffect(() => {
    if (!busy) return;
    const id = setInterval(() => setNow(Date.now()), 250);
    return () => clearInterval(id);
  }, [busy]);

  useEffect(() => {
    if (busy) return;
    const pending = queuedRef.current;
    if (!pending) return;
    queuedRef.current = null;
    setQueued(undefined);
    startRun(pending.prompt, pending.attachSpec);
  }, [busy]);

  const modeKey = kitty === true ? 'M' : 'E';
  const modeAccent = MODE_ACCENT[mode];
  const contentWidth = Math.max(20, width - 6);
  const terminalRows = stdout.rows ?? 24;

  const metrics = engine.client.snapshotMetrics();
  const meter = contextMeter(metrics.compression.inputTokens, engine.state.activeModel, metrics.fallback.activeProvider);
  const meterColor = meter.zone === 'danger' ? PALETTE.error : meter.zone === 'warn' ? PALETTE.warn : PALETTE.muted;
  const contextLabel = metrics.compression.inputTokens > 0 ? `ctx ${meterBar(meter.fraction, 8)} ${Math.round(meter.fraction * 100)}%` : '';
  const compression = metrics.compression.inputTokens > 0 ? `${Math.round((1 - metrics.compression.ratio) * 100)}% ${metrics.compression.strategy.toUpperCase()}` : '';

  const phase = busy && currentTool ? phaseFor(currentTool) : (busy ? 'working' : 'ready');
  const elapsedMs = busy && runStartedAt.current !== null ? Math.max(0, now - runStartedAt.current) : 0;
  const elapsed = busy
    ? (elapsedMs >= 60_000 ? `${Math.floor(elapsedMs / 60_000)}m${String(Math.floor((elapsedMs % 60_000) / 1000)).padStart(2, '0')}s` : `${Math.floor(elapsedMs / 1000)}s`)
    : '';

  const editorLayout = useMemo(() => layoutEditor(edit.value, edit.cursor, inputWidth), [edit.value, edit.cursor, inputWidth]);
  const liveThinkLines = useMemo(() => renderMarkdown(liveThink, contentWidth), [liveThink, contentWidth]);
  const liveAnswerLines = useMemo(() => renderMarkdown(liveAnswer, contentWidth), [liveAnswer, contentWidth]);

  // Cap the streaming region so a long think/answer can't crowd out the chrome;
  // the complete text lands in <Static> once the event fires.
  const liveBudget = Math.max(3, terminalRows - 14 - Math.min(6, taskQueue.length) - Math.min(4, agents.length) - editorLayout.lines.length);
  const liveThinkView = liveThinkLines.slice(-Math.max(2, Math.floor(liveBudget / 2)));
  const liveAnswerView = liveAnswerLines.slice(-liveBudget);

  const doneAgents = agents.filter((lane) => lane.status === 'done').length;

  return <Box flexDirection="column" width={width} paddingX={2}>
    {/* Settled transcript → native scrollback. */}
    <Static key={staticKey} items={lines}>
      {(line, index) => <TranscriptEntry key={index} line={line} width={contentWidth} fallbackModel={engine.state.activeModel} />}
    </Static>

    <Box flexDirection="column">
      {lines.length === 0 && !busy && <Hero width={width} endpoint={engine.client.endpoint ?? 'omniroute'} model={engine.state.activeModel} mode={mode} perm={permMode} />}

      {liveThink !== '' && <Box flexDirection="column" marginTop={1}>
        <Text bold color={PALETTE.warn}>· thinking</Text>
        {liveThinkView.map((segments, index) => <SegmentText key={index} segments={segments} role="thinking" />)}
      </Box>}
      {liveAnswer !== '' && <Box flexDirection="column" marginTop={1}>
        <Text bold color={PALETTE.accent}>◆ {engine.state.activeModel}</Text>
        {liveAnswerView.map((segments, index) => <SegmentText key={index} segments={segments} role="assistant" />)}
      </Box>}

      {toolCards.slice(-5).map((card) => {
        const expanded = expandedTool === card.id;
        const dot = card.status === 'running' ? <Text color={PALETTE.warn}>◍</Text> : card.status === 'error' ? <Text color={PALETTE.error}>✕</Text> : <Text color={PALETTE.success}>✓</Text>;
        const head = card.name === 'run_command'
          ? `$ ${clip(card.target || '…', Math.max(10, contentWidth - 20))}`
          : `${toolVerb(card.name)}${card.target ? ` ${clip(card.target, Math.max(10, contentWidth - 24))}` : ''}`;
        return <Box key={card.id} flexDirection="column">
          <Text dimColor>{dot} {head}{expanded ? '   Ctrl+T collapse' : ''}</Text>
          {expanded && renderToolBody(card, contentWidth, PALETTE)}
        </Box>;
      })}

      {agents.length > 0 && <Box flexDirection="column" borderStyle="round" borderColor={PALETTE.error} paddingX={2} marginTop={1}>
        <Box justifyContent="space-between">
          <Text bold color={PALETTE.error}>⚡ swarm</Text>
          <Text dimColor>{doneAgents}/{agents.length} lanes done</Text>
        </Box>
        {agents.map((lane, index) => {
          const color = AGENT_COLORS[index % AGENT_COLORS.length];
          const glyph = lane.status === 'done' ? '✓' : lane.status === 'error' ? '✕' : lane.status === 'working' ? '◍' : '○';
          const detail = lane.note ?? (lane.label !== lane.id ? lane.label : lane.status);
          return <Text key={lane.id} color={color}>{glyph} {lane.id}  <Text dimColor>{clip(detail, Math.max(12, contentWidth - 8))}</Text></Text>;
        })}
      </Box>}

      {taskQueue.length > 0 && <Box flexDirection="column" borderStyle="round" borderColor={PALETTE.accent} paddingX={2} marginTop={1}>
        <Box justifyContent="space-between">
          <Text bold color={PALETTE.accent}>◇ plan</Text>
          <Text dimColor>{taskQueue.filter((item) => item.status === 'done').length}/{taskQueue.length} done</Text>
        </Box>
        {taskQueue.slice(-6).map((item) => {
          const marker = item.status === 'done' ? '✓' : item.status === 'active' ? '◈' : '○';
          const color = item.status === 'done' ? PALETTE.success : item.status === 'active' ? PALETTE.accent : undefined;
          return <Text key={item.id} color={color} dimColor={item.status === 'done'}>{marker} {clip(item.title, contentWidth - 4)}</Text>;
        })}
      </Box>}

      {engine.state.preview && <Text color={PALETTE.success}>▸ preview live · {engine.state.preview.url}</Text>}

      {sessionsOpen && <Box flexDirection="column" borderStyle="round" borderColor={PALETTE.info} paddingX={2} marginTop={1}>
        <Text bold color={PALETTE.info}>saved sessions</Text>
        <Text dimColor>↑↓ navigate · enter resume · esc close</Text>
        {sessionsList.map((session, index) => (
          <Text key={session.name} color={index === sessionsIndex ? PALETTE.info : undefined}>
            {index === sessionsIndex ? '❯ ' : '  '}{session.name}{session.savedAt ? <Text dimColor>  ·  {session.savedAt}</Text> : null}
          </Text>
        ))}
      </Box>}

      {pickerOpen && <Box flexDirection="column" borderStyle="round" borderColor={PALETTE.accent} paddingX={2} marginTop={1}>
        <Text bold color={PALETTE.accent}>choose an OmniRoute model</Text>
        <Text dimColor>↑↓ / j k navigate · enter select · esc close</Text>
        {pickerError && <Text color={PALETTE.error}>{clip(pickerError, contentWidth)}</Text>}
        {pickerItems.length === 0 && !pickerError && <Text dimColor>no models returned by OmniRoute.</Text>}
        {pickerItems.map((item, index) => {
          const header = index === 0 || pickerItems[index - 1]!.group !== item.group
            ? <Text key={`h-${item.group}`} dimColor bold>{item.group === 'combos' ? 'your combos' : 'auto engine'}</Text>
            : null;
          return <Box key={item.id} flexDirection="column">{header}
            <Text color={index === pickerIndex ? PALETTE.accent : undefined}>{index === pickerIndex ? '❯ ' : '  '}{item.id}{item.strategy ? <Text dimColor>  · {item.strategy}</Text> : null}{item.id === engine.state.activeModel ? '  ✓' : ''}</Text>
          </Box>;
        })}
      </Box>}

      {approval && <Box flexDirection="column" borderStyle="round" borderColor={PALETTE.warn} paddingX={2} marginTop={1}>
        <Text bold color={PALETTE.warn}>approve {approval.tool}?</Text>
        <Text dimColor>args: {clip(JSON.stringify(approval.input), contentWidth)}</Text>
        {approval.scopes.map((scope, index) => <Text key={scope.id} dimColor>  {index + 1} · {clip(scope.label, Math.max(12, contentWidth - 6))}</Text>)}
        <Text dimColor>y allow once · n deny · t always allow · 1–{approval.scopes.length} pick a trust scope</Text>
      </Box>}

      {layoutDebug && <Box flexDirection="column" borderStyle="round" borderColor={PALETTE.muted} paddingX={2} marginTop={1}>
        <Text bold dimColor>layout · {width}×{terminalRows} · Ctrl+L to hide</Text>
        <Text dimColor>static entries {lines.length} · live budget {liveBudget} · think {liveThinkLines.length} · answer {liveAnswerLines.length}</Text>
        <Text dimColor>plan {taskQueue.length} · swarm {agents.length} · tool cards {toolCards.length} · editor rows {editorLayout.lines.length}</Text>
      </Box>}

      {queued && <Text color={PALETTE.warn}>⏎ queued · {clip(queued, Math.max(12, contentWidth - 12))}</Text>}

      <Box borderStyle="round" borderColor={error ? PALETTE.error : modeAccent} paddingX={1} marginTop={1} flexDirection="column">
        {edit.value === ''
          ? <Text color={modeAccent}>❯ <Text dimColor>{busy ? 'type to queue the next task' : 'describe the work and press enter'}</Text></Text>
          : editorLayout.lines.map((text, index) => <Text key={index} color={modeAccent}>{index === 0 ? '❯ ' : '  '}{text}</Text>)}
      </Box>

      {kitty !== null && <Text dimColor>{kitty ? 'kitty protocol active — Shift+Enter makes a new line' : 'this terminal can\'t distinguish Shift+Enter from Enter — use Ctrl+J for a new line'}</Text>}

      <Box flexDirection="column">
        <Box justifyContent="space-between">
          <Text color={busy ? modeAccent : PALETTE.muted}>{busy && <Spinner type="dots" />}{busy ? ' ' : ''}{phase}{elapsed ? ` · ${elapsed}` : ''}{agents.length > 0 ? ` · swarm ${doneAgents}/${agents.length}` : ''}</Text>
          <Text color={PALETTE.muted}>
            <Text color={modeAccent}>{mode}</Text>
            <Text> · </Text>
            <Text color={mode === 'crazy' ? PALETTE.error : PERM_COLOR(permMode)}>{PERM_GLYPH[mode === 'crazy' ? 'bypass' : permMode]} {mode === 'crazy' ? 'bypass' : PERM_LABEL[permMode]}</Text>
            <Text> · {engine.state.activeModel}</Text>
            {metrics.fallback.activeProvider ? <Text> · via {metrics.fallback.activeProvider}</Text> : null}
            {contextLabel ? <Text color={meterColor}> · {contextLabel}</Text> : null}
          </Text>
        </Box>
        <Box justifyContent="space-between">
          <Text dimColor>Enter send · Ctrl+J newline · Ctrl+O models · Ctrl+{modeKey} mode · Shift+Tab perms · Ctrl+T tool · Ctrl+Y copy · Ctrl+C {busy ? 'cancel' : 'quit'} · /help</Text>
          {compression ? <Text dimColor>saved {compression}{metrics.remainingQuota !== undefined ? ` · quota ${metrics.remainingQuota}` : ''}</Text> : null}
        </Box>
      </Box>
    </Box>
  </Box>;
}
