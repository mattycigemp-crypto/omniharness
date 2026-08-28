import type { CompressionTracker, OmniRouteMetrics } from '../types/index.js';

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

export interface ChatMessage {
  role: 'system' | 'user' | 'assistant' | 'tool';
  content: string;
}

export interface ChatResult {
  content: string;
  model: string;
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

  public async chat(model: string, messages: readonly ChatMessage[], signal?: AbortSignal): Promise<ChatResult> {
    const response = await this.request('/chat/completions', {
      method: 'POST',
      signal,
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({ model, messages, stream: false }),
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
    return {
      content: typeof message.content === 'string' ? message.content : '',
      model: answered,
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


