/**
 * Collapsible message groups for the transcript.
 *
 * Pure module: folds runs of tool-role lines into a single synthetic summary
 * line so a long tool trail reads as one line ("▸ 4 tool calls · Ctrl+G to
 * expand") instead of a wall of output. Runs shorter than 3 lines stay
 * unfolded (a lone tool note isn't noise). Expansion is controlled by the
 * caller (Ctrl+G toggles globally); non-tool lines always pass through.
 */

export interface GroupableLine {
  role: 'user' | 'assistant' | 'error' | 'thinking' | 'tool';
  text: string;
  toolName?: string;
}

export interface GroupInfo {
  count: number;
  /** The folded tool texts, shown when the group is expanded. */
  hidden: readonly string[];
}

export interface GroupedLine extends GroupableLine {
  /** Present only on synthetic group-summary lines. */
  group?: GroupInfo;
}

/**
 * Fold runs of tool lines. With `expanded` false, runs of 3+ tool lines
 * collapse to one synthetic summary line carrying `group` metadata.
 *
 * `protectFrom` is a line index (into `lines`) at or after which nothing
 * folds: the "collapse by recency, not kind" rule — the active turn stays
 * fully expanded so nothing about the current decision is hidden, while
 * settled history behind it still compresses. Pass `lines.length` (default)
 * to fold everything, or the index of the last user turn to shield it.
 */
export function foldToolGroups(lines: readonly GroupableLine[], expanded: boolean, protectFrom = lines.length): GroupedLine[] {
  const out: GroupedLine[] = [];
  let run: { line: GroupableLine; index: number }[] = [];
  const flush = (): void => {
    if (run.length === 0) return;
    const runProtected = run.some((entry) => entry.index >= protectFrom);
    if (expanded || runProtected || run.length < 3) {
      out.push(...run.map((entry) => entry.line));
    } else {
      out.push({
        role: 'tool',
        text: `▸ ${run.length} tool calls · Ctrl+G to expand`,
        group: { count: run.length, hidden: run.map((entry) => entry.line.text) },
      });
    }
    run = [];
  };
  lines.forEach((line, index) => {
    if (line.role === 'tool') run.push({ line, index });
    else { flush(); out.push(line); }
  });
  flush();
  return out;
}
