export type TaskStatus = 'idle' | 'planning' | 'running' | 'waiting' | 'completed' | 'failed' | 'cancelled';
export type ToolRisk = 'low' | 'medium' | 'high' | 'critical';

/** High-level working modes that shape the system prompt and allowed skills. */
export type AgentMode = 'plan' | 'build' | 'research' | 'crazy';

/**
 * How tool approvals are handled, independent of the working mode:
 *   - ask         prompt before every high-risk tool call (default)
 *   - acceptEdits  auto-approve file edits; still prompt for commands/previews
 *   - bypass       auto-approve everything
 * CRAZY mode always behaves as `bypass` regardless of this setting.
 */
export type PermissionMode = 'ask' | 'acceptEdits' | 'bypass';

/** A step in the agent's visible task queue. */
export interface TodoItem {
  id: string;
  title: string;
  status: 'pending' | 'active' | 'done';
}

export type TodoAction =
  | { action: 'add'; title: string }
  | { action: 'update'; id: string; title?: string }
  | { action: 'start'; id?: string }
  | { action: 'complete'; id?: string }
  | { action: 'remove'; id: string };

export interface TodoSnapshot {
  todos: readonly TodoItem[];
  updatedAt: string;
}

export interface WorkspaceFile {
  path: string;
  bytes: number;
  kind: 'file' | 'directory';
  extension?: string;
}

export interface WorkspaceState {
  root: string;
  indexedAt: string | null;
  files: readonly WorkspaceFile[];
  gitBranch?: string;
  gitDiffStat?: string;
  contextLocked: boolean;
}

export interface PreviewServer {
  url: string;
  pid?: number;
  startedAt: string;
  command: string;
}

export interface CompressionTracker {
  inputTokens: number;
  compressedTokens: number;
  ratio: number;
  strategy: 'rtk' | 'caveman' | 'none';
  updatedAt: string;
}

export interface FallbackTracker {
  activeProvider?: string;
  /** The model that actually answered, from the `X-OmniRoute-Model` header. */
  model?: string;
  /** Routing strategy the gateway applied, from the `X-OmniRoute-Decision` header. */
  strategy?: string;
  /** Gateway-reported routing/response latency in ms, from `X-OmniRoute-Decision`. */
  latencyMs?: number;
  attempts: number;
  lastFailure?: string;
  cooldownUntil?: string;
}

/**
 * What the gateway reports a completion consumed, read from OmniRoute's
 * cost-telemetry set (`X-OmniRoute-Tokens-In` / `-Tokens-Out` /
 * `-Response-Cost` / `-Latency-Ms`) or, on a stream, from the final usage
 * chunk and the `: x-omniroute-*` metadata trailer.
 */
export interface UsageTracker {
  /** Prompt tokens of the most recent completion — the live size of the context. */
  contextTokens: number;
  /** Prompt tokens summed across the session. */
  tokensIn: number;
  /** Completion tokens summed across the session. */
  tokensOut: number;
  /** USD summed across the session; `0` while every reply was free or unpriced. */
  costUsd: number;
  /** Gateway-reported latency of the most recent completion, ms. */
  latencyMs?: number;
  updatedAt?: string;
}

export interface OmniRouteMetrics {
  compression: CompressionTracker;
  fallback: FallbackTracker;
  usage?: UsageTracker;
  remainingQuota?: number;
  requestCount: number;
}

export interface AgentToolDeclaration<TInput = unknown, TOutput = unknown> {
  name: string;
  description: string;
  risk: ToolRisk;
  inputSchema: unknown;
  execute(input: TInput, signal?: AbortSignal): Promise<TOutput>;
}

/** A single OpenAI function-call request emitted by the model. */
export interface ToolCallRequest {
  id: string;
  type: 'function';
  function: { name: string; arguments: string };
}

/** A multimodal content part accepted by OmniRoute's /chat/completions (modality bridge). */
export type ChatContentPart =
  | { type: 'text'; text: string }
  | { type: 'image_url'; image_url: { url: string } };

/** Wire message shapes accepted by OmniRoute /chat/completions. */
export interface ChatWireMessage {
  role: 'system' | 'user' | 'assistant' | 'tool';
  content: string | readonly ChatContentPart[];
  tool_calls?: readonly ToolCallRequest[];
  tool_call_id?: string;
}

/** A tool call in flight plus its rendered result for the model. */
export interface ToolCallResult {
  call: ToolCallRequest;
  output: string;
}

export interface HarnessState {
  taskStatus: TaskStatus;
  prompt: string;
  mode: AgentMode;
  permissionMode: PermissionMode;
  workspace: WorkspaceState;
  metrics: OmniRouteMetrics;
  activeModel: string;
  messages: readonly HarnessMessage[];
  preview: PreviewServer | null;
  taskQueue: readonly TodoItem[];
  lastTodoUpdate?: TodoSnapshot;
}

export interface HarnessMessage {
  role: 'user' | 'assistant' | 'thought' | 'action' | 'tool' | 'command' | 'error';
  content: string;
  createdAt: string;
  toolName?: string;
  model?: string;
}