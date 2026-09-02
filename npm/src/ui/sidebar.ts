/**
 * Data shaping for the sidebar. The layout lives in the Ink component; every
 * decision it has to make is here so it can be tested without a terminal.
 *
 * The shape follows OpenCode's session sidebar (MIT, sst/opencode): a fixed
 * narrow panel carrying what the conversation column should not have to —
 * session identity, what is running, and what is queued. The implementation is
 * ours; Ink has no scrollbox and no absolute positioning, so the behaviour
 * differs where it has to.
 */

/** Fixed width of the panel, matching the measure OpenCode settled on. */
export const SIDEBAR_WIDTH = 34;

/**
 * Below this the terminal cannot hold a sidebar and a readable conversation
 * at once, so the toggle shows the sidebar *instead of* the conversation
 * rather than beside it. Ink cannot overlay one panel on another, which is
 * what OpenCode does at this size.
 */
export const SIDEBAR_MIN_SPLIT = 92;

export type SidebarMode = 'hidden' | 'split' | 'replace';

/**
 * How the sidebar should be shown, given the terminal width and whether the
 * user has it toggled on.
 */
export function sidebarMode(width: number, wanted: boolean): SidebarMode {
  if (!wanted) return 'hidden';
  return width >= SIDEBAR_MIN_SPLIT ? 'split' : 'replace';
}

/** Width left for the conversation once the sidebar has taken its share. */
export function conversationWidth(width: number, mode: SidebarMode): number {
  if (mode !== 'split') return width;
  return Math.max(20, width - SIDEBAR_WIDTH - 1);
}

export interface Row {
  label: string;
  value: string;
}

/**
 * What the client actually measures. Every field is optional because the
 * TypeScript client tracks context and request count but not output tokens or
 * spend — that lives on the Go side. A row is emitted only for a figure that
 * was really measured, so the panel never shows a cost nobody computed.
 */
export interface UsageSummary {
  tokensIn?: number;
  tokensOut?: number;
  costUSD?: number;
  requests?: number;
}

/**
 * Render a token count the way it is read: exact while small, then thousands.
 * A six-digit number in a 34-column panel is noise, not information.
 */
export function compactTokens(n: number): string {
  if (!Number.isFinite(n) || n < 0) return '0';
  if (n < 1000) return String(Math.round(n));
  if (n < 1_000_000) {
    const k = n / 1000;
    return `${k < 10 ? k.toFixed(1) : Math.round(k)}k`;
  }
  return `${(n / 1_000_000).toFixed(1)}M`;
}

/**
 * Cost to four places under a cent, two above. Sub-cent spend is the normal
 * case for a single turn and "$0.00" would report every one of them as free.
 */
export function formatCost(usd: number): string {
  if (!Number.isFinite(usd) || usd <= 0) return '$0';
  return usd < 0.01 ? `$${usd.toFixed(4)}` : `$${usd.toFixed(2)}`;
}

/**
 * The usage rows, or none at all when nothing has been spent yet. An empty
 * panel section reads as broken; a missing one reads as "not yet".
 */
export function usageRows(usage: UsageSummary): Row[] {
  const rows: Row[] = [];
  const inTokens = usage.tokensIn ?? 0;
  const outTokens = usage.tokensOut ?? 0;
  if (inTokens > 0 || outTokens > 0) {
    // Only name a direction that was measured: "1.2k in · 0 out" claims zero
    // output rather than admitting it is not counted here.
    const parts: string[] = [];
    if (inTokens > 0) parts.push(`${compactTokens(inTokens)} in`);
    if (outTokens > 0) parts.push(`${compactTokens(outTokens)} out`);
    rows.push({ label: 'tokens', value: parts.join(' · ') });
  }
  if ((usage.costUSD ?? 0) > 0) rows.push({ label: 'cost', value: formatCost(usage.costUSD ?? 0) });
  if ((usage.requests ?? 0) > 0) rows.push({ label: 'calls', value: String(usage.requests) });
  return rows;
}

export interface TodoLike {
  title: string;
  status: string;
}

export interface TodoRow {
  marker: string;
  title: string;
  active: boolean;
}

/**
 * Task queue rows. Plain-word markers rather than glyphs, matching the rest of
 * the interface. The active item is marked so the eye finds it without colour,
 * which matters under NO_COLOR.
 */
export function todoRows(todos: readonly TodoLike[], limit: number, width: number): TodoRow[] {
  return todos.slice(0, Math.max(0, limit)).map((todo) => ({
    marker: todo.status === 'done' ? 'x' : todo.status === 'active' ? '>' : '-',
    title: clip(todo.title, Math.max(4, width)),
    active: todo.status === 'active',
  }));
}

/** How many queued items were not shown, for a "+N more" line. */
export function overflowCount(total: number, shown: number): number {
  return Math.max(0, total - shown);
}

export function clip(text: string, width: number): string {
  const runes = [...text.replace(/\s+/g, ' ')];
  if (runes.length <= width) return runes.join('');
  return `${runes.slice(0, Math.max(0, width - 1)).join('')}…`;
}
