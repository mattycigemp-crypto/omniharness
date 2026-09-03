/**
 * Per-model context windows and the context meter.
 *
 * Pure module. OmniRoute's `auto/*` combos route to different underlying
 * models, so the usable context window changes per turn. `windowFor` resolves
 * a model id (or the provider name OmniRoute reports) to a token budget;
 * `contextMeter` turns "tokens in" into a fill fraction and a zone the UI
 * colours (ok / warn / danger) using the playbook's 70 / 90 thresholds.
 *
 * The gateway's catalog is the first source: `/v1/models` states a
 * `context_length` per entry, and `windowIndex` turns that into a lookup the
 * meter consults before the substring table below. The table remains the
 * answer for a model the catalog does not size, and when the catalog could
 * not be read at all.
 */

export type ContextZone = 'ok' | 'warn' | 'danger';

export interface ContextMeter {
  /** 0–1 fraction of the resolved window consumed. */
  fraction: number;
  zone: ContextZone;
  window: number;
  used: number;
}

/** Known windows keyed by a substring of the model / provider id (longest match wins). */
const WINDOWS: ReadonlyArray<readonly [pattern: string, tokens: number]> = [
  ['claude-3-5-haiku', 200_000],
  ['claude-3-5-sonnet', 200_000],
  ['claude-3-7', 200_000],
  ['claude-sonnet-4', 200_000],
  ['claude-opus-4', 200_000],
  ['claude', 200_000],
  ['gpt-4o-mini', 128_000],
  ['gpt-4o', 128_000],
  ['gpt-4.1', 1_047_576],
  ['gpt-5', 400_000],
  ['o3', 200_000],
  ['o4-mini', 200_000],
  ['gemini-1.5-flash', 1_048_576],
  ['gemini-1.5-pro', 2_097_152],
  ['gemini-2.0', 1_048_576],
  ['gemini-2.5-pro', 1_048_576],
  ['gemini', 1_048_576],
  ['grok', 131_072],
  ['deepseek', 128_000],
  ['llama-3.1', 131_072],
  ['llama', 131_072],
  ['mistral', 131_072],
  ['qwen', 131_072],
];

/** Fallback window when nothing matches — conservative so the meter warns early rather than late. */
export const DEFAULT_WINDOW = 128_000;

/** Windows keyed by lower-cased model id. */
export type WindowIndex = ReadonlyMap<string, number>;

/** The catalog shape `windowIndex` reads: an id and, when stated, a window. */
export interface WindowSource {
  id: string;
  contextLength?: number;
}

/**
 * Build the lookup from a catalog. Each entry is keyed by its full id and,
 * when the id carries a provider prefix, by the bare model name after it —
 * `X-OmniRoute-Model` reports the upstream name (`claude-sonnet-4-6`) while
 * the catalog lists it under a prefix (`cc/claude-sonnet-4-6`). The first
 * entry to claim a bare name keeps it, so a `dual`-mode mirror never
 * contradicts its primary.
 */
export function windowIndex(entries: readonly WindowSource[]): WindowIndex {
  const index = new Map<string, number>();
  for (const entry of entries) {
    const tokens = entry.contextLength;
    if (tokens === undefined || !Number.isFinite(tokens) || tokens <= 0) continue;
    const id = entry.id.trim().toLowerCase();
    if (id === '') continue;
    if (!index.has(id)) index.set(id, tokens);
    const slash = id.indexOf('/');
    if (slash > 0 && slash < id.length - 1) {
      const bare = id.slice(slash + 1);
      if (!index.has(bare)) index.set(bare, tokens);
    }
  }
  return index;
}

/**
 * Resolve a context window for a model id and/or the provider OmniRoute
 * reported for the turn. A catalog `known` answers first, by the exact id and
 * then by the bare name after a provider prefix. Otherwise matching is
 * case-insensitive substring; the longest matching pattern wins so
 * `gpt-4o-mini` beats `gpt-4o`.
 */
export function windowFor(modelId: string | undefined, provider?: string, known?: WindowIndex): number {
  if (known && modelId) {
    const id = modelId.trim().toLowerCase();
    const exact = known.get(id);
    if (exact !== undefined) return exact;
    const slash = id.indexOf('/');
    const bare = slash > 0 ? known.get(id.slice(slash + 1)) : undefined;
    if (bare !== undefined) return bare;
  }
  const haystack = `${modelId ?? ''} ${provider ?? ''}`.toLowerCase();
  let best: number | undefined;
  let bestLen = 0;
  for (const [pattern, tokens] of WINDOWS) {
    if (pattern.length > bestLen && haystack.includes(pattern)) {
      best = tokens;
      bestLen = pattern.length;
    }
  }
  return best ?? DEFAULT_WINDOW;
}

/** Build the meter. `used` below 0 clamps to 0; the fraction is capped at 1. */
export function contextMeter(used: number, modelId: string | undefined, provider?: string, known?: WindowIndex): ContextMeter {
  const window = windowFor(modelId, provider, known);
  const safeUsed = Math.max(0, used);
  const fraction = Math.min(1, safeUsed / window);
  const zone: ContextZone = fraction >= 0.9 ? 'danger' : fraction >= 0.7 ? 'warn' : 'ok';
  return { fraction, zone, window, used: safeUsed };
}

/** A compact `[████░░░░░░] 41%` style bar, `cells` wide. */
export function meterBar(fraction: number, cells = 10): string {
  const filled = Math.round(Math.min(1, Math.max(0, fraction)) * cells);
  return `${'█'.repeat(filled)}${'░'.repeat(Math.max(0, cells - filled))}`;
}
