export type TaskStatus = 'idle' | 'planning' | 'running' | 'waiting' | 'completed' | 'failed' | 'cancelled';
export type ToolRisk = 'low' | 'medium' | 'high' | 'critical';

/** High-level working modes that shape the system prompt and allowed skills. */
export type AgentMode = 'plan' | 'build' | 'research';

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
  attempts: number;
  lastFailure?: string;
  cooldownUntil?: string;
}

export interface OmniRouteMetrics {
  compression: CompressionTracker;
  fallback: FallbackTracker;
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

/** Wire message shapes accepted by OmniRoute /chat/completions. */
export interface ChatWireMessage {
  role: 'system' | 'user' | 'assistant' | 'tool';
  content: string;
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
  workspace: WorkspaceState;
  metrics: OmniRouteMetrics;
  activeModel: string;
  messages: readonly HarnessMessage[];
  preview: PreviewServer | null;
}

export interface HarnessMessage {
  role: 'user' | 'assistant' | 'thought' | 'action' | 'tool' | 'command' | 'error';
  content: string;
  createdAt: string;
  toolName?: string;
  model?: string;
}