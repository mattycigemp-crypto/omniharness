export type TaskStatus = 'idle' | 'planning' | 'running' | 'waiting' | 'completed' | 'failed' | 'cancelled';
export type ToolRisk = 'low' | 'medium' | 'high' | 'critical';

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

export interface HarnessState {
  taskStatus: TaskStatus;
  prompt: string;
  workspace: WorkspaceState;
  metrics: OmniRouteMetrics;
  activeModel: string;
  messages: readonly HarnessMessage[];
}

export interface HarnessMessage {
  role: 'user' | 'assistant' | 'thought' | 'action' | 'error';
  content: string;
  createdAt: string;
}
