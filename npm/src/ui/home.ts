/**
 * Data shaping for the home screen — the layout itself lives in the Ink
 * component, but everything it has to decide is pure and lives here so it can
 * be tested without rendering a terminal.
 */

/**
 * Render an age the way someone reads it at a glance: the largest unit that
 * still says something useful, never more than one unit deep.
 *
 * Anything not yet a minute old is "now" rather than "0m ago", because a
 * session saved seconds ago reading as zero looks like a bug.
 */
export function relativeTime(savedAt: string, now: number = Date.now()): string {
  const then = Date.parse(savedAt);
  if (!Number.isFinite(then)) return '';
  const seconds = Math.max(0, Math.round((now - then) / 1000));
  if (seconds < 60) return 'now';
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m ago`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours}h ago`;
  const days = Math.floor(hours / 24);
  if (days < 7) return `${days}d ago`;
  const weeks = Math.floor(days / 7);
  if (weeks < 52) return `${weeks}w ago`;
  return `${Math.floor(days / 365)}y ago`;
}

export interface RecentSession {
  name: string;
  savedAt: string;
}

export interface RecentRow {
  name: string;
  age: string;
}

/**
 * The recent-session rows to show, newest first and capped. Names are clipped
 * rather than wrapped: a single line per session keeps the block a predictable
 * height, which matters because the home screen shares the window with the
 * input.
 */
export function recentRows(sessions: readonly RecentSession[], limit: number, nameWidth: number, now: number = Date.now()): RecentRow[] {
  return sessions.slice(0, Math.max(0, limit)).map((session) => ({
    name: clipName(session.name, nameWidth),
    age: relativeTime(session.savedAt, now),
  }));
}

function clipName(name: string, width: number): string {
  const runes = [...name];
  if (width <= 1 || runes.length <= width) return name;
  return `${runes.slice(0, width - 1).join('')}…`;
}

/**
 * Whether there is room for the two-column home. Below this the columns would
 * be too narrow to hold a session name or a path, so the blocks stack instead.
 */
export const TWO_COLUMN_MIN_WIDTH = 76;

export function twoColumn(width: number): boolean {
  return width >= TWO_COLUMN_MIN_WIDTH;
}

/**
 * Shorten a workspace path from the left, keeping the end. The tail is the
 * part that identifies the project; the head is usually /Users/someone.
 */
export function shortenPath(p: string, width: number): string {
  if (p.length <= width) return p;
  if (width <= 1) return p.slice(-width);
  return `…${p.slice(-(width - 1))}`;
}

/**
 * A one-line summary of what the agent can reach beyond its built-in tools.
 * Says nothing at all when there is nothing to say, rather than "0 skills".
 */
export function capabilityLine(skills: number, plugins: number, mcpTools: number): string {
  const parts: string[] = [];
  if (skills > 0) parts.push(`${skills} skill${skills === 1 ? '' : 's'}${plugins > 0 ? ` from ${plugins} plugin${plugins === 1 ? '' : 's'}` : ''}`);
  if (mcpTools > 0) parts.push(`${mcpTools} mcp tool${mcpTools === 1 ? '' : 's'}`);
  return parts.join(' · ');
}
