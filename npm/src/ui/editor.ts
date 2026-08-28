/** Normalize terminal paste to \n line endings before inserting. */
export function normalizePaste(value: string): string {
  return value.replace(/\r\n?/g, '\n');
}

/** Insert `text` into `value` at 0-based `cursor`, returning the new value and cursor. */
export function insertAt(value: string, cursor: number, text: string): { value: string; cursor: number } {
  const at = clampIndex(cursor, value);
  return { value: value.slice(0, at) + text + value.slice(at), cursor: at + text.length };
}

/** Delete the character just before `cursor` (backspace). */
export function deleteBefore(value: string, cursor: number): { value: string; cursor: number } {
  const at = clampIndex(cursor, value);
  if (at === 0) return { value, cursor: at };
  return { value: value.slice(0, at - 1) + value.slice(at), cursor: at - 1 };
}

/** Delete the character at `cursor` (delete/forward). */
export function deleteAt(value: string, cursor: number): { value: string; cursor: number } {
  const at = clampIndex(cursor, value);
  if (at >= value.length) return { value, cursor: at };
  return { value: value.slice(0, at) + value.slice(at + 1), cursor: at };
}

/** Move the cursor left/right by `delta` (-1 or +1), clamped to bounds. */
export function moveHorizontal(value: string, cursor: number, delta: number): number {
  return clamp(cursor + delta, 0, value.length);
}

/** Index of the start of the logical line containing `cursor`. */
export function lineStartAt(value: string, cursor: number): number {
  const at = clampIndex(cursor, value);
  return value.lastIndexOf('\n', at - 1) + 1;
}

/** Index just past the last char of the logical line containing `cursor`. */
export function lineEndAt(value: string, cursor: number): number {
  const at = clampIndex(cursor, value);
  const nl = value.indexOf('\n', at);
  return nl === -1 ? value.length : nl;
}

interface RowSpan { start: number; end: number }

/** Char-wrap `value` into rendered rows at `width`, matching `layoutEditor`. */
function charRows(value: string, width: number): RowSpan[] {
  const rows: RowSpan[] = [];
  const w = Math.max(1, width);
  let offset = 0;
  for (const para of value.split('\n')) {
    for (let s = 0; s < para.length; s += w) {
      rows.push({ start: offset + s, end: offset + Math.min(s + w, para.length) });
    }
    if (para.length === 0) rows.push({ start: offset, end: offset });
    offset += para.length + 1; // + newline
  }
  if (rows.length === 0) rows.push({ start: 0, end: 0 });
  return rows;
}

/**
 * Move the cursor to the same column on the adjacent rendered row (matching the
 * char-wrapped layout the box renders with). Clamped to the text bounds and to
 * the target row's length.
 */
export function moveVerticalWrapped(value: string, cursor: number, delta: number, width: number): number {
  const at = clampIndex(cursor, value);
  const rows = charRows(value, width);
  let row = rows.length - 1;
  let col = 0;
  for (let r = 0; r < rows.length; r += 1) {
    const span = rows[r]!;
    if (at >= span.start && at <= span.end) { row = r; col = at - span.start; break; }
    if (at < span.start) { row = r; col = 0; break; }
  }
  const target = clamp(row + delta, 0, rows.length - 1);
  const span = rows[target]!;
  return span.start + Math.min(col, span.end - span.start);
}

function clampIndex(cursor: number, value: string): number {
  return clamp(cursor, 0, value.length);
}

function clamp(n: number, min: number, max: number): number {
  return Math.min(max, Math.max(min, n));
}

/** True when a key-value carries a terminal paste (multi-line or multi-char run). */
export function isPaste(value: string): boolean {
  return value.length > 1 || value.includes('\n');
}

export interface SlashCommand {
  command: string;
  arg: string;
}

/** Distinguish a leading slash command (e.g. `/attach file.txt`) from plain text. */
export function parseSlashCommand(text: string): SlashCommand | null {
  const trimmed = text.trim();
  const match = /^\/([a-z][a-z0-9_-]*)(?:\s+(.*))?$/i.exec(trimmed);
  if (!match) return null;
  return { command: match[1]!.toLowerCase(), arg: (match[2] ?? '').trim() };
}

export type AttachKind = 'file' | 'image' | 'video';

export interface AttachmentInput {
  name: string;
  size: number;
  kind: AttachKind;
}

const IMAGE_EXT = /^\.(png|jpe?g|gif|webp|bmp|svg|avif)$/i;
const VIDEO_EXT = /^\.(mp4|webm|mov|mkv|avi)$/i;

export function kindFromName(name: string): AttachKind {
  if (IMAGE_EXT.test(name)) return 'image';
  if (VIDEO_EXT.test(name)) return 'video';
  return 'file';
}

/** Format the attachments block prepended to a prompt so the agent knows what was attached. */
export function attachmentBlock(attachments: readonly AttachmentInput[]): string {
  if (attachments.length === 0) return '';
  const rows = attachments.map((a) => `- ${a.name} (${a.kind}, ${a.size} bytes)`).join('\n');
  return `The user attached these files to this request:\n${rows}\nYou may read_file them from the workspace or user path as needed.\n---\n`;
}

export interface EditorLayout {
  lines: readonly string[];
  cursorLine: number;
  cursorCol: number;
}

/**
 * Lay a multi-line string out across `width` characters per row (char-wrapping
 * exactly like `charRows`, so the caret lands on the correct column) and place
 * a caret marker at the given cursor offset.
 */
export function layoutEditor(text: string, cursor: number, width: number): EditorLayout {
  const rows = charRows(text, width);
  const at = clamp(cursor, 0, text.length);
  let line = rows.length - 1;
  let col = rows[line]!.end - rows[line]!.start;
  for (let r = 0; r < rows.length; r += 1) {
    const span = rows[r]!;
    if (at >= span.start && at <= span.end && !(at === span.end && r + 1 < rows.length && rows[r + 1]!.start === at)) {
      line = r;
      col = at - span.start;
      break;
    }
    if (at < span.start) {
      line = r;
      col = 0;
      break;
    }
  }
  const lines = rows.map((span) => text.slice(span.start, span.end));
  lines[line] = lines[line]!.slice(0, col) + CURSOR + lines[line]!.slice(col);
  return { lines, cursorLine: line, cursorCol: col };
}

const CURSOR = '▍';