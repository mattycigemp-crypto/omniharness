/**
 * Minimal unified-diff detection and segmentation for the TUI.
 *
 * Pure module: given tool output text, decide whether it looks like a
 * unified diff (a file-edit trail such as `git diff` emits) and, if so,
 * split it into styled segments so the Ink renderer can color added lines
 * green and removed lines red. Non-diff text passes through unchanged.
 */

import type { MarkdownSegment } from './markdown.js';

export interface DiffLine {
  /** '+' added, '-' removed, '@' hunk header, ' ' context, 'file' H/---x headers. */
  kind: '+' | '-' | '@' | ' ' | 'file';
  text: string;
}

/** Parse unified-diff text into typed lines. Empty/absent input yields []. */
export function parseDiff(text: string): DiffLine[] {
  const lines = text.split('\n');
  const out: DiffLine[] = [];
  let inDiff = false;
  for (const raw of lines) {
    if (raw.startsWith('diff --git ') || raw.startsWith('Index: ')) {
      continue;
    }
    if (raw.startsWith('+++ ') || raw.startsWith('--- ')) {
      inDiff = true;
      out.push({ kind: 'file', text: raw });
      continue;
    }
    if (raw.startsWith('@@')) {
      inDiff = true;
      out.push({ kind: '@', text: raw });
      continue;
    }
    if (!inDiff) continue;
    const marker = raw[0];
    if (marker === '+') { out.push({ kind: '+', text: raw.slice(1) }); continue; }
    if (marker === '-') { out.push({ kind: '-', text: raw.slice(1) }); continue; }
    if (marker === ' ') { out.push({ kind: ' ', text: raw.slice(1) }); continue; }
    out.push({ kind: ' ', text: raw });
  }
  return out;
}

/** True when the output should be presented as a colored diff. */
export function looksLikeDiff(text: string): boolean {
  return parseDiff(text).some((line) => line.kind === '+' || line.kind === '-');
}

const COLORS: Record<DiffLine['kind'], string | undefined> = {
  '+': 'green',
  '-': 'red',
  '@': 'cyan',
  ' ': undefined,
  file: 'dim',
};

const PREFIX: Record<DiffLine['kind'], string> = {
  '+': '+ ',
  '-': '- ',
  '@': '@ ',
  ' ': '  ',
  file: '',
};

/** Render one diff line into styled segments, preserving indentation exactly. */
function renderLine(line: DiffLine, width: number): MarkdownSegment[][] {
  const color = COLORS[line.kind];
  const styled = (text: string): MarkdownSegment => ({ text, ...(color ? { color } : {}) });
  const body = line.kind === 'file' ? line.text : `${PREFIX[line.kind]}${line.text}`;
  if (body === '') return [[styled('')]];
  // Preserve the raw line verbatim (diff indentation is meaningful); hard-split
  // only when a single line exceeds the column width.
  const rows: MarkdownSegment[][] = [];
  let rest = body;
  while (rest.length > width) {
    rows.push([styled(rest.slice(0, width))]);
    rest = rest.slice(width);
  }
  rows.push([styled(rest)]);
  return rows;
}

/** Render unified-diff text into wrapped styled segments (one array per row). */
export function diffSegments(text: string, width: number): MarkdownSegment[][] {
  const out: MarkdownSegment[][] = [];
  for (const line of parseDiff(text).slice(0, 2000)) {
    out.push(...renderLine(line, width));
  }
  return out;
}