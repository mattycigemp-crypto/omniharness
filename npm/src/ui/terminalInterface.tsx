import React, { useEffect, useMemo, useRef, useState } from 'react';
import { Box, Text, useApp, useInput, useStdin, useStdout } from 'ink';
import Spinner from 'ink-spinner';
import type { MastraEngine, HarnessEvent, ApprovalAction, ApprovalResolution } from '../agent/mastraEngine.js';
import type { AgentMode, HarnessMessage, TodoItem } from '../types/index.js';
import { appendPromptHistory, loadPromptHistory } from '../promptHistory.js';
import { listSessions, loadSnapshot, saveSnapshot, deleteSnapshot } from '../sessionList.js';
import { deleteAt, deleteBefore, insertAt, layoutEditor, lineEndAt, lineStartAt, moveHorizontal, moveVerticalWrapped, normalizePaste } from './editor.js';
import { renderMarkdown, type MarkdownSegment } from './markdown.js';
import { looksLikeDiff, diffSegments } from './diff.js';
import { foldToolGroups, type GroupedLine } from './groups.js';
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
interface Row { key: string; role: LineRole; segments: readonly MarkdownSegment[]; label: string; first: boolean; saved?: string }
type LiveRow =
  | { key: string; kind: 'label'; role: 'thinking' | 'assistant' }
  | { key: string; kind: 'content'; role: 'thinking' | 'assistant'; segments: readonly MarkdownSegment[] };
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

const PALETTE = palette();

function colorFor(role: LineRole, p = PALETTE): string {
  switch (role) {
    case 'user': return p.info;
    case 'error': return p.error;
    case 'thinking': return p.warn;
    case 'tool': return p.muted;
    default: return p.success;
  }
}

function SegmentText({ segments, role }: { segments: readonly MarkdownSegment[]; role: LineRole }): React.ReactElement {
  const base = role === 'thinking' ? PALETTE.warn : role === 'tool' ? PALETTE.muted : role === 'assistant' ? PALETTE.success : role === 'user' ? PALETTE.info : undefined;
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
    // A write trail is the new file content — show it as added (green) lines.
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

/** Render one Line into transcript Rows (label row + content rows). */
function renderLineToRows(line: Line, lineIndex: number, width: number, fallback: string): Row[] {
  const baseLabel = labelFor(line.role, line.model, line.toolName, fallback);
  // Route ribbon: the provider a given answer actually came from is provenance —
  // it belongs on the answer's label, next to the model badge.
  const label = line.role === 'assistant' && line.provider
    ? `${baseLabel}  ·  via ${line.provider}${line.fallback ? ' (failover)' : ''}`
    : baseLabel;
  const markdown = line.role !== 'tool' && line.role !== 'error';
  const wrapped: MarkdownSegment[][] = markdown
    ? renderMarkdown(line.text, width)
    : wrap(line.text, width).map((text) => [{ text }]);
  const next: Row[] = [];
  wrapped.forEach((segments, rowIndex) => next.push({ key: `message-${lineIndex}-${rowIndex}`, role: line.role, segments, label, first: rowIndex === 0 }));
  if (line.saved) next.push({ key: `message-${lineIndex}-saved`, role: 'assistant', segments: [{ text: line.saved }], label: '', first: false, saved: line.saved });
  return next;
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
  const [scrollOffset, setScrollOffset] = useState(0);
  const followTranscriptRef = useRef(true);
  const previousRowCountRef = useRef(0);
  const maxScrollRef = useRef(0);
  const pageSizeRef = useRef(8);
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
  const [approval, setApproval] = useState<ApprovalAction | null>(null);
  const approvalResolve = useRef<((decision: ApprovalResolution) => void) | null>(null);

  const [liveThink, setLiveThink] = useState('');
  const [liveAnswer, setLiveAnswer] = useState('');
  const [kitty, setKitty] = useState<boolean | null>(null);
  const [sessionsList, setSessionsList] = useState<{ name: string; savedAt: string }[]>([]);
  const [sessionsOpen, setSessionsOpen] = useState(false);
  const [sessionsIndex, setSessionsIndex] = useState(0);
  const [groupsExpanded, setGroupsExpanded] = useState(false);
  const [taskQueue, setTaskQueue] = useState<readonly TodoItem[]>(engine.state.taskQueue);
  const [currentTool, setCurrentTool] = useState<string>();
  // Tool activity rendered as collapsible cards: each completed/ongoing tool
  // call carries its target, status, and optional summary/diff trail.
  const [toolCards, setToolCards] = useState<readonly ToolCard[]>([]);
  const [expandedTool, setExpandedTool] = useState<string>();
  // Run timing: monotonic start time plus a ticking elapsed counter while busy.
  const runStartedAt = useRef<number | null>(null);
  const [now, setNow] = useState(() => Date.now());
  // A prompt typed while a run is in flight: stashed here and submitted when the run ends.
  const queuedRef = useRef<{ prompt: string; attachSpec?: string } | null>(null);
  const [queued, setQueued] = useState<string>();
  // Ctrl+L paints the row budget of every layout region over the transcript.
  const [layoutDebug, setLayoutDebug] = useState(false);
  // Restores plain stdout.write when synchronized-output bracketing is torn down.
  const syncRestoreRef = useRef<(() => void) | null>(null);

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
    // The same probe pass asks about synchronized output (DECSET 2026): a positive
    // reply lets us bracket every frame so streaming never tears mid-repaint.
    const onKitty = (chunk: Buffer): void => {
      const text = chunk.toString();
      if (isSyncOutputReply(text) && syncRestoreRef.current === null) {
        syncRestoreRef.current = wrapSynchronizedOutput(stdout as unknown as { write: (c: unknown, ...r: unknown[]) => boolean });
      }
      if (!isKittyQueryResponse(text)) return;
      if (kittyTimer) clearTimeout(kittyTimer);
      stdin?.off('data', onKitty);
      setKitty(true);
    };
    if (stdin) {
      stdin.on('data', onKitty);
      kittyTimer = setTimeout(() => { setKitty(false); stdin.off('data', onKitty); }, 300);
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
          if (event.text) setLines((current) => [...current, { role: 'thinking', text: event.text }]);
          setLiveThink('');
          break;
        case 'text':
          setLines((current) => [...current, {
            role: 'assistant', text: event.content, model: event.model,
            provider: event.provider, fallback: event.fallback,
            saved: event.compression ? `${Math.round((1 - event.compression.ratio) * 100)}% saved (${event.compression.strategy.toUpperCase()}) · ${event.compression.savedTokens.toLocaleString()} tokens` : undefined,
          }]);
          setLiveAnswer('');
          break;
        case 'route':
          setLines((current) => [...current, {
            role: 'tool', toolName: 'route',
            text: event.fallback
              ? `route · failover → ${event.provider ?? 'unknown'} (attempt ${event.attempts + 1})${event.reason ? ` · ${event.reason}` : ''}`
              : `route · ${event.provider ?? 'unknown'}`,
          }]);
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
          setLines((current) => [...current, { role: 'tool', text: `preview: ${event.url}`, toolName: 'preview', url: event.url }]);
          break;
        case 'attach':
          setLines((current) => [...current, { role: 'tool', text: `attached: ${event.name} (${event.kind}, ${event.size} bytes)`, toolName: 'attach' }]);
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
      stdin?.off('data', onKitty);
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
    setLines((current) => [...current, { role: 'tool', text: `mode → ${next}`, toolName: 'mode' }]);
  };

  const approve = (approved: boolean, trust?: string): void => {
    const resolve = approvalResolve.current;
    setApproval(null); approvalResolve.current = null;
    resolve?.({ approved, trust });
  };

  /** Copy the most recent assistant reply (or last transcript line) to the clipboard via OSC 52. */
  const yankLastBlock = (): void => {
    const target = [...lines].reverse().find((entry) => entry.role === 'assistant') ?? lines[lines.length - 1];
    if (!target) return;
    const seq = osc52Copy(target.text);
    if (!seq) {
      setLines((current) => [...current, { role: 'tool', text: 'clipboard: block too large to copy', toolName: 'clipboard' }]);
      return;
    }
    try {
      stdout.write(seq);
      setLines((current) => [...current, { role: 'tool', text: `copied ${target.text.length} chars to clipboard`, toolName: 'clipboard' }]);
    } catch { /* clipboard write is best-effort */ }
  };

  /** Kick off an engine run for `prompt`, attaching `attachSpec` files first when given. */
  const startRun = (prompt: string, attachSpec?: string): void => {
    followTranscriptRef.current = true;
    setScrollOffset(0);
    setEdit({ value: '', cursor: 0 }); setBusy(true); setError(undefined);
    setToolCards([]);
    setExpandedTool(undefined);
    if (runStartedAt.current === null) runStartedAt.current = Date.now();
    setNow(Date.now());
    historyIdxRef.current = -1;
    if (prompt) {
      syncPromptHistory([prompt, ...promptHistoryRef.current.filter((entry) => entry !== prompt)].slice(0, 200));
      void appendPromptHistory(prompt).catch(() => { /* best-effort */ });
    }
    if (!attachSpec) setLines((current) => [...current, { role: 'user', text: prompt }]);
    void (async (): Promise<void> => {
      try {
        if (attachSpec) await engine.attach(attachSpec.split(/\s+/).filter(Boolean));
        await engine.run(prompt);
        /* answer is streamed live via text_delta / text events */
      } catch (reason: unknown) {
        const message = reason instanceof Error ? reason.message : String(reason);
        setError(message); setLines((current) => [...current, { role: 'error', text: message }]);
      } finally {
        const startedAt = runStartedAt.current;
        setBusy(false);
        setCurrentTool(undefined);
        setToolCards((current) => current.map((card) => card.status === 'running' ? { ...card, status: 'error' } : card));
        runStartedAt.current = null;
        setLiveThink('');
        setLiveAnswer('');
        // A long run probably pulled focus elsewhere: nudge with a bell + OSC 9 notification.
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

  /** Whether ↑/↓ should browse prompt history instead of moving the text caret. */
  const browsingHistory = (): boolean => historyIdxRef.current >= 0 || edit.value === '';

  // Kept current each render so jumpToLine (defined before the row layout is
  // computed) can resolve a transcript-line index to a scroll position.
  const allRowsRef = useRef<readonly Row[]>([]);
  const storedHeightRef = useRef(0);

  /** Scroll so the first rendered row of transcript line `lineIndex` sits at the top. */
  const jumpToLine = (lineIndex: number): void => {
    const rows = allRowsRef.current;
    const targetRow = rows.findIndex((row) => row.key.startsWith(`message-${lineIndex}-`));
    if (targetRow < 0) return;
    followTranscriptRef.current = false;
    const desired = rows.length - targetRow - storedHeightRef.current;
    setScrollOffset(clamp(desired, 0, maxScrollRef.current));
  };

  /** Chapters: one per user turn, titled by the prompt's first line. */
  const chapters = (): { index: number; title: string }[] =>
    lines.flatMap((line, index) => line.role === 'user'
      ? [{ index, title: line.text.split('\n')[0]!.slice(0, 60) || '(empty prompt)' }]
      : []);

  /** Scroll by rendered transcript rows; positive values move toward older output. */
  const scrollTranscript = (delta: number): void => {
    if (delta > 0 && maxScrollRef.current > 0) followTranscriptRef.current = false;
    setScrollOffset((current) => {
      const next = clamp(current + delta, 0, maxScrollRef.current);
      if (next === 0 && delta < 0) followTranscriptRef.current = true;
      return next;
    });
  };

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
              if (snapshot == null) {
                setLines((current) => [...current, { role: 'error', text: `snapshot ${selected.name} is unreadable` }]);
                return;
              }
              setLines(snapshot.messages.map(lineFromMessage));
              setTaskQueue(snapshot.taskQueue);
              setLines((current) => [...current, { role: 'tool', text: `session resumed: ${selected.name} (${snapshot.messages.length} messages)`, toolName: 'sessions' }]);
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
              { role: 'tool', text: '/sessions — list saved sessions (enter to resume)', toolName: 'commands' },
              { role: 'tool', text: '/save <name> — snapshot the current session', toolName: 'commands' },
              { role: 'tool', text: '/forget <name> — delete a saved session', toolName: 'commands' },
              { role: 'tool', text: '/attach <files> — attach files to the next message', toolName: 'commands' },
              { role: 'tool', text: '/find <text> — jump to the most recent line containing <text>', toolName: 'commands' },
              { role: 'tool', text: '/chapters — list turns and jump: /chapters <n>', toolName: 'commands' },
              { role: 'tool', text: 'keys: Ctrl+O models · Ctrl+E mode · Ctrl+C cancel · PgUp/PgDn scroll · Ctrl+G fold tool groups · Ctrl+T tool card · Ctrl+Y copy last reply · Ctrl+L layout budget · ↑/↓ prompt history', toolName: 'commands' },
              { role: 'tool', text: 'a prompt typed while a run is working is queued and sent when it finishes', toolName: 'commands' },
            ]);
            setEdit({ value: '', cursor: 0 }); historyIdxRef.current = -1;
            return;
          }
          const findMatch = /^\/find\s+(.+)$/.exec(raw);
          if (findMatch) {
            const needle = findMatch[1]!.toLowerCase();
            const hits = lines.map((line, index) => ({ line, index })).filter(({ line }) => line.text.toLowerCase().includes(needle));
            const last = hits[hits.length - 1];
            setLines((current) => [...current, {
              role: 'tool', toolName: 'find',
              text: last ? `find "${findMatch[1]}" · ${hits.length} match${hits.length === 1 ? '' : 'es'} · jumped to the most recent` : `no match for "${findMatch[1]}"`,
            }]);
            if (last) jumpToLine(last.index);
            setEdit({ value: '', cursor: 0 }); historyIdxRef.current = -1;
            return;
          }
          const chapterJump = /^\/chapters?\s+(\d+)$/.exec(raw);
          if (chapterJump) {
            const list = chapters();
            const pick = list[Number(chapterJump[1]) - 1];
            if (pick) jumpToLine(pick.index);
            setLines((current) => [...current, { role: 'tool', toolName: 'chapters', text: pick ? `jumped to chapter ${chapterJump[1]}: ${pick.title}` : `no chapter ${chapterJump[1]}` }]);
            setEdit({ value: '', cursor: 0 }); historyIdxRef.current = -1;
            return;
          }
          if (raw === '/chapters' || raw === '/chapter') {
            const list = chapters();
            setLines((current) => [...current,
              ...(list.length === 0 ? [{ role: 'tool' as const, toolName: 'chapters', text: 'no chapters yet — each prompt starts one' }]
                : list.map((chapter, order) => ({ role: 'tool' as const, toolName: 'chapters', text: `${order + 1}. ${chapter.title}` }))),
              ...(list.length > 0 ? [{ role: 'tool' as const, toolName: 'chapters', text: '/chapters <n> to jump' }] : []),
            ]);
            setEdit({ value: '', cursor: 0 }); historyIdxRef.current = -1;
            return;
          }
          if (raw === '/sessions') {
            void listSessions(engine.state.workspace.root).then((sessions) => {
              setSessionsList(sessions);
              setSessionsIndex(0);
              setSessionsOpen(sessions.length > 0);
              if (sessions.length === 0) {
                setLines((current) => [...current, { role: 'tool', text: 'no saved sessions — use /save <name> to snapshot this one', toolName: 'sessions' }]);
              }
            });
            setEdit({ value: '', cursor: 0 });
            return;
          }
          const saveMatch = /^\/save\s+([\w.-]+)$/.exec(raw);
          if (saveMatch) {
            const name = saveMatch[1]!;
            void saveSnapshot(engine.state.workspace.root, name, {
              messages: [...engine.state.messages], taskQueue: [...engine.state.taskQueue], savedAt: new Date().toISOString(),
            }).then(() => {
              setLines((current) => [...current, { role: 'tool', text: `session saved: ${name}`, toolName: 'sessions' }]);
            }).catch((reason: unknown) => {
              setLines((current) => [...current, { role: 'error', text: `save failed: ${reason instanceof Error ? reason.message : String(reason)}` }]);
            });
            setEdit({ value: '', cursor: 0 });
            return;
          }
          const delMatch = /^\/forget\s+([\w.-]+)$/.exec(raw);
          if (delMatch) {
            void deleteSnapshot(engine.state.workspace.root, delMatch[1]!).then(() => {
              setLines((current) => [...current, { role: 'tool', text: `session deleted: ${delMatch[1]}`, toolName: 'sessions' }]);
            });
            setEdit({ value: '', cursor: 0 });
            return;
          }
          if (raw === '/clear') {
            historyIdxRef.current = -1;
            followTranscriptRef.current = true;
            setScrollOffset(0);
            setTaskQueue([]);
            setError(undefined);
            setCurrentTool(undefined);
            setToolCards([]);
            setExpandedTool(undefined);
            runStartedAt.current = null;
            setLiveThink('');
            setLiveAnswer('');
            syncPromptHistory([]);
            setLines([]);
            void engine.clearHistory().catch(() => { /* /clear resets local state; persistence is best-effort */ });
            setEdit({ value: '', cursor: 0 });
            return;
          }
          const prompt = attachMatch ? '' : raw;
          const attachSpec = attachMatch ? attachMatch[1]! : undefined;
          if (!prompt && !attachSpec) return;
          // Input stays live during a run: a prompt typed now is queued, not dropped,
          // and fires the moment the current run ends.
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
        // While a run is in flight, Ctrl+C cancels that run; when idle it quits.
        if (busy) { engine.cancel(); return; }
        exit();
        return;
      case 'ctrlM': cycleMode(); return;
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

  // Legacy keys Ink parses correctly (\r, \n, \x08, ESC[A arrows, ctrl+letters) plus text and paste.
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
    if (key.pageUp || (key.ctrl && value.toLowerCase() === 'u')) { scrollTranscript(pageSizeRef.current); return; }
    if (key.pageDown || (key.ctrl && value.toLowerCase() === 'd')) { scrollTranscript(-pageSizeRef.current); return; }
    if (key.ctrl && value.toLowerCase() === 'l') { setLayoutDebug((current) => !current); return; }
    if (key.ctrl && value.toLowerCase() === 'y') { yankLastBlock(); return; }
    // Ctrl+T toggles the most recent tool card between collapsed and expanded.
    if (key.ctrl && value.toLowerCase() === 't') {
      const latest = toolCards[toolCards.length - 1];
      setExpandedTool((current) => (latest && current === latest.id) ? undefined : (latest ? latest.id : undefined));
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
  // Live statusline fields: model, mode, running phase, tokens/context, elapsed.
  const runningTool = toolCards[toolCards.length - 1];
  const phase = busy && currentTool ? phaseFor(currentTool) : (busy ? 'working' : 'ready');
  const elapsedMs = busy && runStartedAt.current !== null
    ? Math.max(0, now - runStartedAt.current)
    : 0;
  const elapsed = busy
    ? (elapsedMs >= 60_000 ? `${Math.floor(elapsedMs / 60_000)}m${String(Math.floor((elapsedMs % 60_000) / 1000)).padStart(2, '0')}s` : `${Math.floor(elapsedMs / 1000)}s`)
    : '';
  const meter = contextMeter(metrics.compression.inputTokens, engine.state.activeModel, metrics.fallback.activeProvider);
  const meterColor = meter.zone === 'danger' ? PALETTE.error : meter.zone === 'warn' ? PALETTE.warn : PALETTE.muted;
  const contextLabel = metrics.compression.inputTokens > 0
    ? `ctx ${meterBar(meter.fraction, 8)} ${Math.round(meter.fraction * 100)}%`
    : '';
  const contentWidth = Math.max(20, width - 8);
  const terminalRows = stdout.rows ?? 24;
  const editorLayout = useMemo(() => layoutEditor(edit.value, edit.cursor, inputWidth), [edit.value, edit.cursor, inputWidth]);
  const liveThinkLines = useMemo(() => renderMarkdown(liveThink, contentWidth), [liveThink, contentWidth]);
  const liveAnswerLines = useMemo(() => renderMarkdown(liveAnswer, contentWidth), [liveAnswer, contentWidth]);
  const liveRows = useMemo<readonly LiveRow[]>(() => {
    const rows: LiveRow[] = [];
    if (liveThink !== '') {
      rows.push({ key: 'live-think-label', kind: 'label', role: 'thinking' });
      liveThinkLines.forEach((segments, index) => rows.push({ key: `live-think-${index}`, kind: 'content', role: 'thinking', segments }));
    }
    if (liveAnswer !== '') {
      rows.push({ key: 'live-answer-label', kind: 'label', role: 'assistant' });
      liveAnswerLines.forEach((segments, index) => rows.push({ key: `live-answer-${index}`, kind: 'content', role: 'assistant', segments }));
    }
    return rows;
  }, [liveThink, liveAnswer, liveThinkLines, liveAnswerLines]);

  // Cache markdown/wrapping per Line object. Streaming updates create new live
  // text, but completed historical lines retain their rendered rows.
  const rowCacheRef = useRef<{ width: number; fallback: string; rows: WeakMap<Line, readonly Row[]> } | null>(null);
  const allRows = useMemo<Row[]>(() => {
    const fallback = engine.state.activeModel;
    let cache = rowCacheRef.current;
    if (cache === null || cache.width !== contentWidth || cache.fallback !== fallback) {
      cache = { width: contentWidth, fallback, rows: new WeakMap<Line, readonly Row[]>() };
      rowCacheRef.current = cache;
    }
    const out: Row[] = [];
    lines.forEach((line, lineIndex) => {
      let rendered = cache.rows.get(line);
      if (rendered === undefined) {
        rendered = renderLineToRows(line, lineIndex, contentWidth, fallback);
        cache.rows.set(line, rendered);
      }
      out.push(...rendered);
    });
    return out;
  }, [lines, contentWidth, engine.state.activeModel]);

  // Fold tool-role rows into collapsible groups. Expansion is global (Ctrl+G);
  // folded groups render as a single summary line instead of a wall of output.
  const displayRows = useMemo<Row[]>(() => {
    const lastUserIdx = lines.map((line) => line.role).lastIndexOf('user');
    const folded = foldToolGroups(
      lines.map((line) => ({ role: line.role, text: line.text, toolName: line.toolName })),
      groupsExpanded,
      lastUserIdx >= 0 ? lastUserIdx : lines.length,
    );
    const out: Row[] = [];
    const fallback = engine.state.activeModel;
    folded.forEach((gline, lineIndex) => {
      const isGroup = 'group' in gline && gline.group !== undefined;
      const key = isGroup ? `group-${lineIndex}` : `message-${lineIndex}`;
      if (isGroup) {
        out.push({
          key, role: 'tool',
          segments: [{ text: gline.text, dim: true }],
          label: labelFor('tool', undefined, 'group'), first: true,
        });
        return;
      }
      const line = gline as Line;
      const rendered = renderLineToRows(line, lineIndex, contentWidth, fallback);
      out.push(...rendered);
    });
    return out;
  }, [lines, contentWidth, engine.state.activeModel, groupsExpanded]);

  const planRows = taskQueue.length > 0 ? 6 + Math.min(6, taskQueue.length) : 0;
  const pickerGroups = new Set(pickerItems.map((item) => item.group)).size;
  const pickerRows = pickerOpen ? 6 + pickerItems.length + pickerGroups + (pickerError || pickerItems.length === 0 ? 1 : 0) : 0;
  const approvalRows = approval ? 8 : 0;
  const inputRows = 3 + editorLayout.lines.length;
  const footerRows = 3;
  const chromeRows = 3 + footerRows + inputRows + (kitty !== null ? 1 : 0) + planRows + pickerRows + approvalRows;
  const messageHeight = Math.max(3, terminalRows - chromeRows);
  const toolRows = toolCards.length > 0 ? toolCards.length : 0; // one collapsed card per tool call
  const statusRows = (engine.state.preview ? 1 : 0) + (busy ? 1 : 0) + toolRows;
  const storedHeight = Math.max(0, messageHeight - Math.min(messageHeight, liveRows.length + statusRows));
  const liveHeight = Math.max(0, messageHeight - storedHeight - statusRows);
  const visibleLiveRows = liveHeight > 0 ? liveRows.slice(-liveHeight) : [];
  const maxScroll = Math.max(0, allRows.length - storedHeight);
  maxScrollRef.current = maxScroll;
  allRowsRef.current = allRows;
  storedHeightRef.current = storedHeight;
  pageSizeRef.current = Math.max(1, storedHeight - 2);
  const boundedScroll = clamp(scrollOffset, 0, maxScroll);
  const endRow = allRows.length - boundedScroll;
  const startRow = Math.max(0, endRow - storedHeight);
  const visibleRows = allRows.slice(startRow, endRow);
  const hiddenAbove = startRow;
  const hiddenBelow = boundedScroll;
  const scrollStatus = allRows.length === 0
    ? 'transcript empty'
    : storedHeight === 0
      ? `live output · ${allRows.length} transcript rows`
      : hiddenBelow === 0
        ? `showing rows ${startRow + 1}-${endRow} of ${allRows.length} · following latest`
        : `showing rows ${startRow + 1}-${endRow} of ${allRows.length} · ${hiddenAbove} older · ${hiddenBelow} newer`;

  useEffect(() => {
    const previous = previousRowCountRef.current;
    const added = allRows.length - previous;
    previousRowCountRef.current = allRows.length;
    if (previous === 0 || added <= 0) return;
    if (followTranscriptRef.current) {
      setScrollOffset(0);
    } else {
      setScrollOffset((current) => clamp(current + added, 0, maxScrollRef.current));
    }
  }, [allRows.length]);

  useEffect(() => {
    setScrollOffset((current) => clamp(current, 0, maxScroll));
  }, [maxScroll]);

  // Tick a clock while a run is in flight so the statusline can show elapsed time
  // without re-rendering on every event. runStartedAt is set when a run begins.
  useEffect(() => {
    if (!busy) return;
    const id = setInterval(() => setNow(Date.now()), 250);
    return () => clearInterval(id);
  }, [busy]);

  // Drain a prompt that was queued while the previous run was in flight.
  useEffect(() => {
    if (busy) return;
    const pending = queuedRef.current;
    if (!pending) return;
    queuedRef.current = null;
    setQueued(undefined);
    startRun(pending.prompt, pending.attachSpec);
  }, [busy]);

  return <Box flexDirection="column" width={width} height={terminalRows} paddingX={2} overflow="hidden">
    <Box justifyContent="space-between" paddingY={1}>
      <Text bold color="cyan">OMNIHARNESS <Text dimColor>· {mode} mode</Text></Text>
      <Text dimColor>OMNIROUTE :20128</Text>
    </Box>
    <Box flexDirection="column" height={messageHeight} overflow="hidden">
      {lines.length === 0 && <Box flexDirection="column" marginTop={2}><Text color="cyan" bold>Ready when you are.</Text><Text dimColor>Describe the work. OmniHarness routes it through your OmniRoute account. Ctrl+{modeKey} cycles plan · build · research · crazy.</Text></Box>}
      {visibleRows.map((row, index) => row.saved
        ? <Text key={row.key} dimColor>{row.saved}</Text>
        : row.first
          ? <Box key={row.key} flexDirection="column" marginTop={index === 0 ? 0 : 1}>
              <Text color={colorFor(row.role)} bold>{row.label}</Text>
              <SegmentText segments={row.segments} role={row.role} />
            </Box>
          : <SegmentText key={row.key} segments={row.segments} role={row.role} />)}
      {visibleLiveRows.map((row) => row.kind === 'label'
        ? <Text key={row.key} color={colorFor(row.role)} bold>{row.role === 'thinking' ? 'think' : engine.state.activeModel}</Text>
        : <SegmentText key={row.key} segments={row.segments} role={row.role} />)}
      {toolCards.slice(-6).map((card) => {
        const expanded = expandedTool === card.id;
        const status = card.status === 'running' ? <Text color={PALETTE.warn}>• running</Text> : card.status === 'error' ? <Text color={PALETTE.error}>✕ error</Text> : <Text color={PALETTE.success}>✓ done</Text>;
        const head = card.name === 'run_command'
          ? `$ ${clip(card.target || '…', Math.max(10, contentWidth - 26))}`
          : `${toolVerb(card.name)}${card.target ? ` · ${clip(card.target, Math.max(10, contentWidth - 30))}` : ''}`;
        const badge = expanded ? '▾' : '▸';
        return <Box key={card.id} flexDirection="column">
          <Text dimColor>{badge} {head} · {status}{expanded ? '  ·  Ctrl+T to collapse' : ''}</Text>
          {expanded && renderToolBody(card, contentWidth, PALETTE)}
        </Box>;
      })}
      {engine.state.preview && <Text color="green" dimColor>preview live · {engine.state.preview.url}</Text>}
      {busy && <Text color="cyan"><Spinner type="dots" /> working in {mode} mode on {engine.state.activeModel}{currentTool ? ` · now ${currentTool}` : ''}</Text>}
      {queued && <Text color={PALETTE.warn}>⏎ queued · {clip(queued, Math.max(12, contentWidth - 12))}</Text>}
    </Box>
    {taskQueue.length > 0 && <Box flexDirection="column" borderStyle="round" borderColor="cyan" paddingX={2} paddingY={1} marginTop={1}>
      <Box justifyContent="space-between"><Text bold color="cyan">plan</Text><Text dimColor>{taskQueue.filter((item) => item.status === 'done').length}/{taskQueue.length} done</Text></Box>
      {taskQueue.slice(-6).map((item) => {
        const marker = item.status === 'done' ? '✓' : item.status === 'active' ? '▸' : '○';
        const color = item.status === 'done' ? 'green' : item.status === 'active' ? 'cyan' : undefined;
        return <Text key={item.id} color={color} dimColor={item.status === 'done'}>{marker} {clip(item.title, contentWidth - 4)}</Text>;
      })}
    </Box>}
    {sessionsOpen && <Box flexDirection="column" borderStyle="round" borderColor="blue" paddingX={2} paddingY={1}>
      <Text bold color="blue">Saved sessions</Text>
      <Text dimColor>↑↓ navigate · enter resume · esc close</Text>
      {sessionsList.map((session, index) => (
        <Text key={session.name} color={index === sessionsIndex ? 'blue' : undefined}>
          {index === sessionsIndex ? '› ' : '  '}{session.name}{session.savedAt ? <Text dimColor>  ·  {session.savedAt}</Text> : null}
        </Text>
      ))}
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
      <Text dimColor>args: {clip(JSON.stringify(approval.input), contentWidth)}</Text>
      {approval.scopes.map((scope, index) => <Text key={scope.id} dimColor>  {index + 1} · {clip(scope.label, Math.max(12, contentWidth - 6))}</Text>)}
      <Text dimColor>y allow once · n deny · t always allow · 1–{approval.scopes.length} pick a trust scope</Text>
    </Box>}
    {layoutDebug && <Box flexDirection="column" borderStyle="round" borderColor="gray" paddingX={2}>
      <Text bold dimColor>layout · {width}×{terminalRows} · Ctrl+L to hide</Text>
      <Text dimColor>chrome {chromeRows} · input {inputRows} · plan {planRows} · overlay {pickerRows + approvalRows} · caps {kitty !== null ? 1 : 0}</Text>
      <Text dimColor>message {messageHeight} = stored {storedHeight} + live {liveHeight} + status {statusRows}</Text>
      <Text dimColor>transcript rows {allRows.length} · scroll {boundedScroll}/{maxScroll} · page {pageSizeRef.current}</Text>
    </Box>}
    <Box borderStyle="round" borderColor={error ? 'red' : 'cyan'} paddingX={1} marginTop={1} flexDirection="column">
      {edit.value === ''
        ? <Text color="cyan">› <Text dimColor>type a task and press enter</Text></Text>
        : editorLayout.lines.map((text, index) => <Text key={index} color="cyan">{index === 0 ? '› ' : '  '}{text}</Text>)}
    </Box>
    {kitty !== null && <Text dimColor>{kitty ? 'kitty protocol active — Shift+Enter makes a new line' : 'this terminal can\'t distinguish Shift+Enter from Enter — use Ctrl+J for a new line'}</Text>}
    <Box flexDirection="column">
      {/* Live statusline: phase/elapsed left, model/mode/context + scroll position right. */}
      <Box justifyContent="space-between">
        <Text color={busy ? PALETTE.info : PALETTE.muted}>{busy && <Spinner type="dots" />}{phase}{elapsed ? ` · ${elapsed}` : ''}</Text>
        <Text color={PALETTE.muted}>{engine.state.activeModel} · {mode}{contextLabel ? <Text color={meterColor}> · {contextLabel}</Text> : null} · {scrollStatus}</Text>
      </Box>
      <Box justifyContent="space-between"><Text dimColor>Enter send · Ctrl+J new line · Ctrl+O models · Ctrl+{modeKey} mode · Ctrl+T tool · Ctrl+Y copy · Ctrl+L layout · Ctrl+C cancel/quit · /help</Text>{hud.length > 0 && <Text dimColor>{hud.join(' · ')}</Text>}</Box>
    </Box>
  </Box>;
}