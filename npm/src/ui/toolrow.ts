/**
 * Tool row composition.
 *
 * Follows the two shapes OpenCode uses (MIT, anomalyco/opencode): a one-line
 * inline row with a fixed-width status column so descriptions align down the
 * page, and a block form for output, marked with a rule down its left edge
 * rather than boxed on all four sides — a full border around every tool
 * result turns a transcript into a stack of crates.
 */

export type ToolStatus = 'running' | 'done' | 'error' | 'denied';

/**
 * Width of the status column. Every marker is padded to it, so the tool
 * descriptions start at the same column whatever happened to the call. The
 * markers were 'ok' (2), 'FAIL' (4) and '..' (2), which meant every failure
 * shunted its own description two columns right.
 */
// Five, not four: the longest marker is 'FAIL', and padding to its own
// length leaves no gap, so a failed call rendered as "FAIL$ go test ...".
// The extra column is the separator, which is why the head is not padded
// again on the other side.
export const STATUS_WIDTH = 5;

/** Plain-word status marker, padded to a fixed column. No glyphs. */
export function statusMarker(status: ToolStatus): string {
  switch (status) {
    case 'running': return '..'.padEnd(STATUS_WIDTH);
    case 'error': return 'FAIL'.padEnd(STATUS_WIDTH);
    case 'denied': return 'no'.padEnd(STATUS_WIDTH);
    default: return 'ok'.padEnd(STATUS_WIDTH);
  }
}

/**
 * The text beside the marker: a verb and its target, clipped to the room
 * actually left once the status column and the indent are accounted for.
 * Sizing this to the full width is what makes rows wrap and the column
 * collapse.
 */
export function toolHead(verb: string, target: string, width: number, reserve = 0): string {
  // `reserve` is room kept for something drawn after the head on the same
  // row — the collapse hint, today. Without it the hint pushed itself onto a
  // line of its own, which is worse than not showing it.
  const room = Math.max(8, width - STATUS_WIDTH - 2 - Math.max(0, reserve));
  if (target === '') return clip(verb, room);
  const head = `${verb} ${target}`;
  return clip(head, room);
}

export function clip(text: string, width: number): string {
  const flat = text.replace(/\s+/g, ' ');
  const runes = [...flat];
  if (runes.length <= width) return flat;
  return `${runes.slice(0, Math.max(0, width - 1)).join('')}…`;
}
