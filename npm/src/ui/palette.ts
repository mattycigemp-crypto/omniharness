/**
 * Semantic color palette for the TUI.
 *
 * Pure module: exposes one set of named roles (accent, muted, success, warn,
 * error, info) as Ink color values. When the terminal advertises truecolor
 * (COLORTERM=truecolor|24bit) the roles resolve to 24-bit hex; otherwise they
 * fall back to the closest ANSI-16 named colors so the UI never depends on
 * truecolor support and never renders unreadable/gray secondary text.
 */

export interface Palette {
  accent: string;
  muted: string;
  success: string;
  warn: string;
  error: string;
  info: string;
}

/** Truecolor palette (24-bit hex values Ink passes to chalk.hex). */
const TRUE_COLORS: Palette = {
  accent: '#7aa2f7',
  muted: '#8b93a7',
  success: '#9ece6a',
  warn: '#e0af68',
  error: '#f7768e',
  info: '#2ac3de',
};

/** ANSI-16 fallback used when the terminal has no truecolor support. */
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

/** Resolve the semantic palette for the current terminal environment. */
export function palette(env: Record<string, string | undefined> = process.env): Palette {
  return supportsTrueColor(env) ? { ...TRUE_COLORS } : { ...ANSI_16 };
}