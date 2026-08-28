import type { ChatWireMessage, CompressionTracker, OmniRouteMetrics, ToolCallRequest } from '../types/index.js';

/** Deltas surfaced incrementally while a streaming completion is in flight. */
export type StreamDelta =
  | { type: 'reasoning'; delta: string }
  | { type: 'text'; delta: string }
  | { type: 'tool_call'; call: ToolCallRequest };

export interface ChatStreamOptions {
  signal?: AbortSignal;
  tools?: readonly ChatTool[];
  onDelta?: (delta: StreamDelta) => void;
}

export interface OmniRouteConfig {
  endpoint?: string;
  apiKey?: string;
  timeoutMs?: number;
}

export interface OmniRouteCombo {
  name: string;
  strategy?: string;
  models: readonly { model?: string; providerId?: string; kind?: string }[];
  isDefault?: boolean;
}

export type ChatMessage = ChatWireMessage;

export interface ChatTool {
  type: 'function';
  function: { name: string; description?: string; parameters: unknown };
}

export interface ChatResult {
  content: string;
  model: string;
  finishReason: 'stop' | 'tool_calls' | 'length' | 'content_filter' | string;
  reasoning?: string;
  toolCalls?: readonly ToolCallRequest[];
  usage?: { inputTokens: number; outputTokens: number; totalTokens: number };
  headers: Headers;
}

export class OmniRouteError extends Error {
  public constructor(public readonly status: number, message: string) {
    super(`OmniRoute ${status}: ${message}`);
    this.name = 'OmniRouteError';
  }
}

const DEFAULT_ENDPOINT = 'http://localhost:20128';

export class OmniRouteClient {
  public readonly endpoint: string;
  private readonly timeoutMs: number;
  private apiKey: string;
  private readonly metrics: OmniRouteMetrics = {
    compression: { inputTokens: 0, compressedTokens: 0, ratio: 1, strategy: 'none', updatedAt: new Date().toISOString() },
    fallback: { attempts: 0 },
    requestCount: 0,
  };

  public constructor(config: OmniRouteConfig = {}) {
    this.endpoint = (config.endpoint ?? process.env.OMNIROUTE_URL ?? DEFAULT_ENDPOINT).replace(/\/$/, '');
    this.apiKey = config.apiKey ?? process.env.OMNIROUTE_API_KEY ?? '';
    this.timeoutMs = config.timeoutMs ?? 120_000;
  }

  public setApiKey(apiKey: string): void {
    this.apiKey = apiKey.trim();
  }

  public snapshotMetrics(): OmniRouteMetrics {
    return structuredClone(this.metrics);
  }

  public async listCombos(signal?: AbortSignal): Promise<readonly OmniRouteCombo[]> {
    const response = await this.request('/v1/combos', { method: 'GET', signal });
    const payload: unknown = await response.json();
    if (Array.isArray(payload)) return payload as OmniRouteCombo[];
    if (this.isRecord(payload) && Array.isArray(payload.combos)) return payload.combos as OmniRouteCombo[];
    if (this.isRecord(payload) && Array.isArray(payload.data)) return payload.data as OmniRouteCombo[];
    throw new OmniRouteError(response.status, 'invalid combos response');
  }

  /** Embed one or more texts via the gateway; returns one vector per input. */
  public async embed(inputs: readonly string[], model = 'gemini-embedding-001', signal?: AbortSignal): Promise<number[][]> {
    const response = await this.request('/v1/embeddings', {
      method: 'POST',
      signal,
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({ model, input: inputs }),
    });
    const payload: unknown = await response.json();
    if (!this.isRecord(payload) || !Array.isArray(payload.data)) throw new OmniRouteError(response.status, 'invalid embeddings response');
    return payload.data.map((entry: unknown) => {
      if (!this.isRecord(entry) || !Array.isArray(entry.embedding)) throw new OmniRouteError(response.status, 'invalid embeddings response');
      return entry.embedding.map((value: unknown) => this.number(value));
    });
  }

  /** List every model id the gateway exposes, including `auto/*` virtual combos and individual providers. */
  public async listModels(signal?: AbortSignal): Promise<readonly string[]> {
    const response = await this.request('/v1/models', { method: 'GET', signal });
    const payload: unknown = await response.json();
    const data = this.isRecord(payload) && Array.isArray(payload.data) ? payload.data : [];
    const ids: string[] = [];
    for (const entry of data) {
      if (this.isRecord(entry) && typeof entry.id === 'string' && entry.id.trim() !== '') ids.push(entry.id);
    }
    if (ids.length === 0) throw new OmniRouteError(response.status, 'invalid models response');
    return ids;
  }

  /** Stream a completion, surfacing deltas as they arrive and returning the accumulated result. */
  public async chatStream(model: string, messages: readonly ChatMessage[], options: ChatStreamOptions = {}): Promise<ChatResult> {
    const response = await this.request('/chat/completions', {
      method: 'POST',
      signal: options.signal,
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({ model, messages, stream: true, tools: options.tools }),
    });
    const usage: { inputTokens: number; outputTokens: number; totalTokens: number } = { inputTokens: 0, outputTokens: 0, totalTokens: 0 };
    let finishReason: ChatResult['finishReason'] = 'stop';
    let content = '';
    let reasoning = '';
    let answered = model;
    let lineBuffer = '';
    // Partially-accumulated tool calls keyed by stream index.
    const toolStreams = new Map<number, { id: string; name: string; argsFragments: string[] }>();

    const flushData = (line: string): void => {
      const sep = line.indexOf(':');
      if (sep === -1) return;
      if (line.slice(0, sep) !== 'data') return;
      const payload = line.slice(sep + 1).trim();
      if (payload === '[DONE]') return;
      const chunk = this.safeParse(payload);
      if (!chunk || !Array.isArray(chunk.choices)) return;
      if (typeof chunk.model === 'string' && chunk.model !== '' && chunk.model !== 'omniroute') answered = chunk.model;
      if (this.isRecord(chunk.usage)) {
        usage.inputTokens = this.number(chunk.usage.prompt_tokens);
        usage.outputTokens = this.number(chunk.usage.completion_tokens);
        usage.totalTokens = this.number(chunk.usage.total_tokens);
      }
      for (const choice of chunk.choices) {
        if (!this.isRecord(choice)) continue;
        if (typeof choice.finish_reason === 'string' && choice.finish_reason !== '') finishReason = choice.finish_reason;
        const delta = this.isRecord(choice.delta) ? choice.delta : {};
        if (typeof delta.reasoning === 'string' && delta.reasoning !== '') {
          reasoning += delta.reasoning;
          options.onDelta?.({ type: 'reasoning', delta: delta.reasoning });
        }
        if (typeof delta.content === 'string' && delta.content !== '') {
          content += delta.content;
          options.onDelta?.({ type: 'text', delta: delta.content });
        }
        if (Array.isArray(delta.tool_calls)) {
          for (const raw of delta.tool_calls) {
            if (!this.isRecord(raw)) continue;
            const idx = typeof raw.index === 'number' ? raw.index : 0;
            const existing = toolStreams.get(idx) ?? { id: '', name: '', argsFragments: [] };
            const fn = this.isRecord(raw.function) ? raw.function : {};
            if (typeof raw.id === 'string' && raw.id !== '') existing.id = raw.id;
            if (typeof fn.name === 'string' && fn.name !== '') existing.name = fn.name;
            if (typeof fn.arguments === 'string' && fn.arguments !== '') existing.argsFragments.push(fn.arguments);
            toolStreams.set(idx, existing);
            // Whole-argument chunks (as produced by OmniRoute) complete a call immediately.
            if (existing.id !== '' && existing.name !== '' && existing.argsFragments.some((frag) => this.isValidJson(frag))) {
              const call: ToolCallRequest = { id: existing.id, type: 'function', function: { name: existing.name, arguments: existing.argsFragments.join('') } };
              options.onDelta?.({ type: 'tool_call', call });
            }
          }
        }
      }
    };

    const body = response.body;
    if (body) {
      const reader = body.getReader();
      const decoder = new TextDecoder();
      try {
        for (;;) {
          const { done, value } = await reader.read();
          if (done) break;
          lineBuffer += decoder.decode(value, { stream: true });
          let newline: number;
          while ((newline = lineBuffer.indexOf('\n')) !== -1) {
            const line = lineBuffer.slice(0, newline);
            lineBuffer = lineBuffer.slice(newline + 1);
            const trimmed = line.replace(/\r$/, '');
            if (trimmed.trim() !== '') flushData(trimmed);
          }
        }
      } finally {
        reader.releaseLock();
      }
    } else {
      // Non-streaming fallback when no body stream is exposed.
      const fallback = await this.chat(model, messages, options);
      return fallback;
    }

    const toolCalls: ToolCallRequest[] = [...toolStreams.values()]
      .filter((entry) => entry.id !== '' && entry.name !== '')
      .map((entry) => ({ id: entry.id, type: 'function', function: { name: entry.name, arguments: entry.argsFragments.join('') } }));
    return { content, model: answered, finishReason, reasoning: reasoning || undefined, toolCalls, usage, headers: response.headers };
  }

  public async chat(model: string, messages: readonly ChatMessage[], options: { signal?: AbortSignal; tools?: readonly ChatTool[] } = {}): Promise<ChatResult> {
    const response = await this.request('/chat/completions', {
      method: 'POST',
      signal: options.signal,
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({ model, messages, stream: false, tools: options.tools }),
    });
    const payload: unknown = await response.json();
    if (!this.isRecord(payload) || !Array.isArray(payload.choices) || payload.choices.length === 0) {
      throw new OmniRouteError(response.status, 'invalid chat response');
    }
    const choice = payload.choices[0];
    const message = this.isRecord(choice) && this.isRecord(choice.message) ? choice.message : {};
    const usage = this.isRecord(payload.usage) ? payload.usage : undefined;
    // OpenAI-compatible responses report the model that actually answered
    // (the combo may have routed anywhere); fall back to the requested id.
    const answered = typeof payload.model === 'string' && payload.model.trim() !== '' ? payload.model : model;
    const toolCalls = Array.isArray(message.tool_calls) ? message.tool_calls.map((call: unknown) => this.asToolCall(call)).filter((call): call is ToolCallRequest => call !== null) : undefined;
    return {
      content: typeof message.content === 'string' ? message.content : '',
      model: answered,
      finishReason: typeof choice.finish_reason === 'string' ? choice.finish_reason : 'stop',
      reasoning: typeof message.reasoning === 'string' ? message.reasoning : undefined,
      toolCalls,
      usage: usage ? {
        inputTokens: this.number(usage.prompt_tokens),
        outputTokens: this.number(usage.completion_tokens),
        totalTokens: this.number(usage.total_tokens),
      } : undefined,
      headers: response.headers,
    };
  }

  private async request(path: string, init: RequestInit): Promise<Response> {
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), this.timeoutMs);
    const signal = init.signal ? this.combineSignals(init.signal, controller.signal) : controller.signal;
    const headers = new Headers(init.headers);
    if (this.apiKey) headers.set('authorization', `Bearer ${this.apiKey}`);
    headers.set('x-omniharness-client', 'omniharness');
    headers.set('x-omniharness-metrics', 'tokens,compression,fallback,quota');
    this.metrics.requestCount += 1;
    try {
      const response = await fetch(`${this.endpoint}${path}`, { ...init, headers, signal });
      this.updateMetrics(response.headers);
      if (!response.ok) throw new OmniRouteError(response.status, await this.safeBody(response));
      return response;
    } finally {
      clearTimeout(timer);
    }
  }

  private updateMetrics(headers: Headers): void {
    const compression = this.metrics.compression;
    const input = this.headerNumber(headers, 'x-omniroute-input-tokens');
    const compressed = this.headerNumber(headers, 'x-omniroute-compressed-tokens');
    if (input !== undefined && compressed !== undefined && input > 0) {
      compression.inputTokens = input;
      compression.compressedTokens = compressed;
      compression.ratio = compressed / input;
      compression.strategy = (headers.get('x-omniroute-compression') as CompressionTracker['strategy'] | null) ?? 'none';
      compression.updatedAt = new Date().toISOString();
    }
    const fallback = this.metrics.fallback;
    fallback.activeProvider = headers.get('x-omniroute-provider') ?? fallback.activeProvider;
    fallback.lastFailure = headers.get('x-omniroute-fallback-reason') ?? fallback.lastFailure;
    const attempts = this.headerNumber(headers, 'x-omniroute-fallback-attempts');
    if (attempts !== undefined) fallback.attempts = attempts;
    const cooldown = headers.get('x-omniroute-cooldown-until');
    if (cooldown) fallback.cooldownUntil = cooldown;
    const quota = this.headerNumber(headers, 'x-omniroute-remaining-quota');
    if (quota !== undefined) this.metrics.remainingQuota = quota;
  }

  private async safeBody(response: Response): Promise<string> {
    const body = await response.text();
    return body.slice(0, 500).replace(this.apiKey, '[REDACTED]');
  }

  private headerNumber(headers: Headers, name: string): number | undefined {
    const value = headers.get(name);
    if (value === null) return undefined;
    const parsed = Number(value);
    return Number.isFinite(parsed) ? parsed : undefined;
  }

  private number(value: unknown): number {
    return typeof value === 'number' && Number.isFinite(value) ? value : 0;
  }

  private safeParse(text: string): Record<string, unknown> | null {
    try { return JSON.parse(text) as Record<string, unknown>; }
    catch { return null; }
  }

  private isValidJson(text: string): boolean {
    try { JSON.parse(text); return true; }
    catch { return false; }
  }

  private asToolCall(value: unknown): ToolCallRequest | null {
    if (!this.isRecord(value) || typeof value.id !== 'string' || value.type !== 'function') return null;
    const fn = this.isRecord(value.function) ? value.function : {};
    if (typeof fn.name !== 'string' || typeof fn.arguments !== 'string') return null;
    return { id: value.id, type: 'function', function: { name: fn.name, arguments: fn.arguments } };
  }

  private isRecord(value: unknown): value is Record<string, unknown> {
    return typeof value === 'object' && value !== null;
  }

  private combineSignals(first: AbortSignal, second: AbortSignal): AbortSignal {
    const controller = new AbortController();
    const abort = (): void => controller.abort();
    if (first.aborted || second.aborted) {
      controller.abort();
      return controller.signal;
    }
    first.addEventListener('abort', abort, { once: true });
    second.addEventListener('abort', abort, { once: true });
    return controller.signal;
  }
}


