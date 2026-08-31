/**
 * Markdown → styled segments for the terminal chat.
 *
 * Pure functions: parse the common chat markdown subset (bold, italic,
 * strikethrough, inline code, links, headings, lists, blockquotes, code
 * fences, GFM tables, rules) into lines of styled segments, word-wrapped
 * to a column width. Unclosed constructs degrade to plain text.
 */

import { highlightCode } from './highlight.js';

export interface MarkdownSegment {
  text: string;
  bold?: boolean;
  italic?: boolean;
  strikethrough?: boolean;
  underline?: boolean;
  dim?: boolean;
  color?: string;
  /** Keep the whole text as one unit through word-splitting (e.g. `[ ]`). */
  atomic?: boolean;
}

interface StyledWord {
  text: string;
  style: Omit<MarkdownSegment, 'text'>;
}

type Align = 'left' | 'right' | 'center';

const TOKEN_RE = /(\~\~[^~]+\~\~|\*\*[^*]+\*\*|`[^`\n]+`|\*[^*\n]+\*|\[[^\]]+\]\([^)\s]+\))/g;
const DELIM_RE = /^\s*:?-+:?\s*$/;
const BULLETS = ['•', '◦', '▪'] as const;
const BOX_RE = /^\[([ xX])\]\s*(.*)$/;

/** Parse inline formatting into styled segments. */
function inline(text: string): MarkdownSegment[] {
  const out: MarkdownSegment[] = [];
  let last = 0;
  for (const match of text.matchAll(TOKEN_RE)) {
    const index = match.index ?? 0;
    if (index > last) out.push({ text: text.slice(last, index) });
    const token = match[0];
    if (token.startsWith('~~')) out.push({ text: token.slice(2, -2), strikethrough: true });
    else if (token.startsWith('**')) out.push({ text: token.slice(2, -2), bold: true });
    else if (token.startsWith('`')) out.push({ text: token.slice(1, -1), color: 'cyan' });
    else if (token.startsWith('[')) {
      const close = token.indexOf('](');
      out.push({ text: token.slice(1, close), underline: true, color: 'blue' });
      out.push({ text: ` ${token.slice(close + 2, -1)}`, dim: true });
    } else {
      out.push({ text: token.slice(1, -1), italic: true });
    }
    last = index + token.length;
  }
  if (last < text.length) out.push({ text: text.slice(last) });
  return out;
}

function wordsOf(segments: readonly MarkdownSegment[]): StyledWord[] {
  const words: StyledWord[] = [];
  for (const segment of segments) {
    const { text, atomic, ...style } = segment;
    if (atomic) { words.push({ text, style }); continue; }
    for (const word of text.split(' ')) {
      if (word !== '') words.push({ text: word, style });
    }
  }
  return words;
}

/** Greedily fill lines up to `width`, hard-breaking words that exceed it. */
function wrapWords(words: readonly StyledWord[], width: number): MarkdownSegment[][] {
  const lines: MarkdownSegment[][] = [];
  let line: MarkdownSegment[] = [];
  let lineLen = 0;
  const flush = (): void => {
    if (line.length > 0) { lines.push(line); line = []; lineLen = 0; }
  };
  for (const word of words) {
    let rest = word.text;
    while (rest.length > width) {
      if (lineLen > 0) flush();
      line.push({ text: rest.slice(0, width), ...word.style });
      rest = rest.slice(width);
      flush();
    }
    if (rest === '') continue;
    const gap = lineLen > 0 ? 1 : 0;
    if (lineLen + gap + rest.length <= width) {
      if (gap > 0) line.push({ text: ' ', ...word.style });
      line.push({ text: rest, ...word.style });
      lineLen += gap + rest.length;
    } else {
      flush();
      line.push({ text: rest, ...word.style });
      lineLen = rest.length;
    }
  }
  flush();
  return lines;
}

function hardSlice(text: string, width: number): string[] {
  const out: string[] = [];
  let rest = text;
  while (rest.length > width) { out.push(rest.slice(0, width)); rest = rest.slice(width); }
  out.push(rest);
  return out;
}

const plainWidth = (segments: readonly MarkdownSegment[]): number => segments.reduce((n, s) => n + s.text.length, 0);

/** Split a table row on unescaped pipes, stripping the cells created by leading/trailing pipes. */
function splitRow(line: string): string[] {
  const cells: string[] = [];
  let current = '';
  for (let i = 0; i < line.length; i += 1) {
    const ch = line[i];
    if (ch === '\\' && line[i + 1] === '|') { current += '|'; i += 1; }
    else if (ch === '|') { cells.push(current); current = ''; }
    else current += ch;
  }
  cells.push(current);
  const trimmed = cells.map((cell) => cell.trim());
  if (trimmed[0] === '' && line.startsWith('|')) trimmed.shift();
  if (trimmed[trimmed.length - 1] === '' && line.endsWith('|')) trimmed.pop();
  return trimmed;
}

function alignOf(cell: string): Align {
  const value = cell.trim();
  const left = value.startsWith(':');
  const right = value.endsWith(':');
  if (left && right) return 'center';
  if (right) return 'right';
  return 'left';
}

/** Render a validated GFM table as aligned lines of styled segments. */
function renderTable(header: string[], aligns: readonly Align[], body: readonly (readonly string[])[], width: number): MarkdownSegment[][] {
  const cols = header.length;
  const rows: (readonly (readonly MarkdownSegment[])[])[] = [header, ...body].map((row) =>
    Array.from({ length: cols }, (_, c) => inline(row[c] ?? '')),
  );
  const colWidths = Array.from({ length: cols }, (_, c) =>
    Math.max(3, ...rows.map((row) => plainWidth(row[c] ?? []))),
  );
  let total = colWidths.reduce((n, w) => n + w, 0) + 3 * cols + 1;
  while (total > width) {
    const widest = colWidths.indexOf(Math.max(...colWidths));
    if (colWidths[widest] <= 3) break;
    colWidths[widest] -= 1;
    total -= 1;
  }
  const out: MarkdownSegment[][] = [];
  rows.forEach((row, r) => {
    const wrapped = row.map((cell, c) => wrapWords(wordsOf(cell), colWidths[c]));
    const height = Math.max(1, ...wrapped.map((cellLines) => cellLines.length));
    for (let l = 0; l < height; l += 1) {
      const segments: MarkdownSegment[] = [];
      for (let c = 0; c < cols; c += 1) {
        const line = wrapped[c]?.[l] ?? [];
        const pad = Math.max(0, colWidths[c] - plainWidth(line));
        segments.push({ text: '│ ' });
        if (aligns[c] === 'right') segments.push({ text: ' '.repeat(pad) }, ...line);
        else if (aligns[c] === 'center') segments.push({ text: ' '.repeat(Math.floor(pad / 2)) }, ...line, { text: ' '.repeat(Math.ceil(pad / 2)) });
        else segments.push(...line, { text: ' '.repeat(pad) });
        segments.push({ text: ' ' });
      }
      segments.push({ text: '│' });
      out.push(r === 0 ? segments.map((s) => ({ ...s, bold: true, color: s.color ?? 'cyan' })) : segments);
    }
    if (r === 0) out.push([{ text: `├${colWidths.map((w) => '─'.repeat(w + 2)).join('┼')}┤`, dim: true }]);
  });
  return out;
}

/**
 * Render markdown text as wrapped lines of styled segments.
 * Code fences are preserved verbatim (hard-sliced to width, not word-wrapped).
 * Tables require a header row with pipes and a matching delimiter row, so a
 * stray pipe in prose is never rendered as a table.
 */
export function renderMarkdown(text: string, width: number): MarkdownSegment[][] {
  const out: MarkdownSegment[][] = [];
  const src = text.replace(/\r/g, '').split('\n');
  let i = 0;
  while (i < src.length) {
    const line = src[i];
    const fence = /^```\s*([A-Za-z0-9_+-]*)\s*$/.exec(line);
    if (fence) {
      const code: string[] = [];
      i += 1;
      while (i < src.length && !/^```/.test(src[i])) { code.push(src[i]); i += 1; }
      i += 1; // skip closing fence
      out.push(...highlightCode(code, fence[1] === '' ? undefined : fence[1], width));
      continue;
    }
    if (/^\s*$/.test(line)) { i += 1; continue; }
    const heading = /^(#{1,6})\s+(.*)$/.exec(line);
    if (heading) {
      const segments = inline(heading[2]).map((s) => ({ ...s, bold: true, color: s.color ?? 'cyan' }));
      out.push(...wrapWords(wordsOf(segments), width));
      i += 1;
      continue;
    }
    if (/^\s*([-*_])\s*([-*_])\s*([-*_])\s*$/.test(line)) {
      out.push([{ text: '─'.repeat(width), dim: true }]);
      i += 1;
      continue;
    }
    if (/^\s*>\s?/.test(line)) {
      const quote = line.replace(/^\s*>\s?/, '');
      const segments = inline(quote).map((s) => ({ ...s, dim: true }));
      out.push(...wrapWords(wordsOf([{ text: '│ ' }, ...segments]), width));
      i += 1;
      continue;
    }
    const list = /^(\s*)([-*+]|\d+[.)])\s+(.*)$/.exec(line);
    if (list) {
      const level = Math.min(Math.floor(list[1].length / 2), BULLETS.length - 1);
      const marker = /^\d/.test(list[2]) ? list[2] : BULLETS[level];
      const box = BOX_RE.exec(list[3]);
      const segments: MarkdownSegment[] = [{ text: `${'  '.repeat(level)}${marker}`, atomic: true }];
      if (box) {
        const checked = box[1] !== ' ';
        segments.push({ text: checked ? '[x]' : '[ ]', atomic: true, ...(checked ? { color: 'green' } : { dim: true }) });
        segments.push(...inline(box[2]));
      } else {
        segments.push(...inline(list[3]));
      }
      out.push(...wrapWords(wordsOf(segments), width));
      i += 1;
      continue;
    }
    if (i + 1 < src.length && line.includes('|')) {
      const header = splitRow(line);
      const delimiter = splitRow(src[i + 1]);
      if (header.length > 0 && delimiter.length === header.length && delimiter.every((cell) => DELIM_RE.test(cell))) {
        const aligns = delimiter.map(alignOf);
        const body: string[][] = [];
        i += 2;
        while (i < src.length && src[i].includes('|') && !/^\s*$/.test(src[i])) {
          body.push(splitRow(src[i]));
          i += 1;
        }
        out.push(...renderTable(header, aligns, body, width));
        continue;
      }
    }
    const paragraph: string[] = [line];
    i += 1;
    while (i < src.length
      && !/^\s*$/.test(src[i])
      && !/^```/.test(src[i])
      && !/^(#{1,6})\s/.test(src[i])
      && !/^\s*([-*_])\s*([-*_])\s*([-*_])\s*$/.test(src[i])
      && !/^\s*>\s?/.test(src[i])
      && !/^(\s*)([-*+]|\d+[.)])\s+/.test(src[i])) {
      paragraph.push(src[i]);
      i += 1;
    }
    out.push(...wrapWords(wordsOf(inline(paragraph.join(' '))), width));
  }
  return out;
}
