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
  if (value.charCodeAt(at - 1) === 0x0a) return { value: value.slice(0, at - 1) + value.slice(at), cursor: at - 1 };
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

/** Visual index of the caret on the current line (0-based). */
function columnAt(value: string, cursor: number): number {
  const at = clampIndex(cursor, value);
  const lineStart = value.lastIndexOf('\n', at - 1) + 1;
  return at - lineStart;
}

/** Move the cursor to the same visual column on an adjacent line. Returns clamped index. */
export function moveVertical(value: string, cursor: number, delta: number): number {
  const at = clampIndex(cursor, value);
  const target = columnAt(value, at);
  if (delta < 0) {
    const lineStart = value.lastIndexOf('\n', at - 1);
    if (lineStart === -1) return 0;
    const prevStart = value.lastIndexOf('\n', lineStart - 1) + 1;
    const upTo = lineStart + 1;
    return prevStart + Math.min(target, upTo - prevStart - 1);
  }
  const nextNl = value.indexOf('\n', at);
  if (nextNl === -1) return value.length;
  const nextStart = nextNl + 1;
  const nextEnd = value.indexOf('\n', nextStart);
  const lineEnd = nextEnd === -1 ? value.length : nextEnd;
  return nextStart + Math.min(target, lineEnd - nextStart);
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
 * exactly so the caret lands on the correct column) and place a `cursorChar`
 * caret at the given cursor offset.
 */
export function layoutEditor(text: string, cursor: number, width: number): EditorLayout {
  const w = Math.max(1, width);
  const at = clamp(cursor, 0, text.length);
  const lines: string[] = [];
  let cursorLine = 0;
  let cursorCol = 0;
  let placed = false;

  const paragraphs = text.split('\n');
  let offset = 0;
  for (let p = 0; p < paragraphs.length; p += 1) {
    const para = paragraphs[p]!;
    for (let start = 0; start <= para.length; start += w) {
      const chunk = para.slice(start, start + w);
      const chunkStart = offset + start;
      const chunkEnd = chunkStart + chunk.length;
      if (!placed && at >= chunkStart && at <= chunkEnd && !(at === chunkEnd && para.length > chunkEnd)) {
        lines.push(chunk.slice(0, at - chunkStart) + CURSOR + chunk.slice(at - chunkStart));
        placed = true;
        cursorLine = lines.length - 1;
        cursorCol = at - chunkStart;
        continue;
      }
      lines.push(chunk);
    }
    offset += para.length + 1; // + newline
    if (p < paragraphs.length - 1) {
      // place caret on an empty trailing row when it sits exactly at a newline
      if (!placed && at === offset - 1) {
        if (cursorLine < lines.length) lines[cursorLine] = lines[cursorLine]! + (para.length === 0 || para.length % w === 0 ? CURSOR : '');
        placed = true;
      } else {
        lines.push('');
      }
    }
  }
  if (!placed) {
    lines.push(CURSOR);
    cursorLine = lines.length - 1;
    cursorCol = 0;
  }
  return { lines, cursorLine, cursorCol };
}

const CURSOR = '▍';