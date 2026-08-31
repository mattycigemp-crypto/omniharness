/**
 * Semantic color palette + theme resolution for the TUI.
 *
 * Pure module: exposes one set of named roles (accent, muted, success, warn,
 * error, info) as Ink color values. Theme selection:
 *   - NO_COLOR (any value)  → ANSI-16 named colors, no truecolor
 *   - OMNIHARNESS_THEME     → 'light' | 'dark' (default 'dark')
 *   - COLORTERM             → truecolor (24-bit hex) when 'truecolor'|'24bit'
 * Light themes swap the hex set only; ANSI-16 fallbacks are shared so the UI
 * never depends on truecolor support and never renders unreadable text.
 */

export interface Palette {
  accent: string;
  muted: string;
  success: string;
  warn: string;
  error: string;
  info: string;
}

export type ThemeName = 'dark' | 'light';

/**
 * Dark truecolor palette — OmniRoute "signal" identity: a teal accent (routing,
 * throughput) over cool slate neutrals, with a distinct sky-blue for user input.
 */
const DARK_TRUE: Palette = {
  accent: '#2dd4bf',
  muted: '#8b93a7',
  success: '#8fd66f',
  warn: '#e6b955',
  error: '#f2637e',
  info: '#56b6ff',
};

/** Light truecolor palette — darker text on light backgrounds, same hue roles. */
const LIGHT_TRUE: Palette = {
  accent: '#2f5fd7',
  muted: '#5d6470',
  success: '#3f7d23',
  warn: '#9a6a10',
  error: '#c2344f',
  info: '#0a7ea4',
};

/** ANSI-16 fallback shared by both themes (named colors adapt per terminal). */
const ANSI_16: Palette = {
  accent: 'blue',
  muted: 'gray',
  success: 'green',
  warn: 'yellow',
  error: 'red',
  info: 'cyan',
};

/** Whether the terminal supports 24-bit color per the COLORTERM convention. */
export function supportsTrueColor(env: Record<string, string | undefined> = process.env): boolean {
  const colorTerm = env.COLORTERM;
  return colorTerm === 'truecolor' || colorTerm === '24bit';
}

/** Resolve the theme name: explicit setting wins, then dark default. */
export function themeName(env: Record<string, string | undefined> = process.env): ThemeName {
  return env.OMNIHARNESS_THEME === 'light' ? 'light' : 'dark';
}

/** Resolve the semantic palette for the current terminal environment. */
export function palette(env: Record<string, string | undefined> = process.env): Palette {
  if (env.NO_COLOR !== undefined && env.NO_COLOR !== '') return { ...ANSI_16 };
  if (!supportsTrueColor(env)) return { ...ANSI_16 };
  return { ...(themeName(env) === 'light' ? LIGHT_TRUE : DARK_TRUE) };
}