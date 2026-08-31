/**
 * Per-model context windows and the context meter.
 *
 * Pure module. OmniRoute's `auto/*` combos route to different underlying
 * models, so the usable context window changes per turn. `windowFor` resolves
 * a model id (or the provider name OmniRoute reports) to a token budget;
 * `contextMeter` turns "tokens in" into a fill fraction and a zone the UI
 * colours (ok / warn / danger) using the playbook's 70 / 90 thresholds.
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

/**
 * Resolve a context window for a model id and/or the provider OmniRoute
 * reported for the turn. Matching is case-insensitive substring; the longest
 * matching pattern wins so `gpt-4o-mini` beats `gpt-4o`.
 */
export function windowFor(modelId: string | undefined, provider?: string): number {
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
export function contextMeter(used: number, modelId: string | undefined, provider?: string): ContextMeter {
  const window = windowFor(modelId, provider);
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
