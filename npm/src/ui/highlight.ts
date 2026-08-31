/**
 * Tiny syntax highlighter for code fences in the TUI.
 *
 * Pure module: no dependencies, no regex state, degrade-to-plain semantics.
 * Covers the small set of languages real agent output uses most
 * (js/ts/tsx/jsx, python, json, bash/sh); unknown languages fall back to
 * plain text so nothing renders worse than before.
 */

import type { MarkdownSegment } from './markdown.js';

/** Languages we can highlight; anything else renders plain. */
export function highlightable(lang: string | undefined): boolean {
  return lang !== undefined && HIGHLIGHTERS.has(normalize(lang));
}

function normalize(lang: string): string {
  const l = lang.toLowerCase();
  if (l === 'javascript' || l === 'node' || l === 'jsx') return 'js';
  if (l === 'typescript' || l === 'tsx') return 'ts';
  if (l === 'py') return 'python';
  if (l === 'sh' || l === 'shell' || l === 'zsh') return 'bash';
  return l;
}

const HIGHLIGHTERS = new Set(['js', 'ts', 'python', 'json', 'bash']);

/** Comment-to-end-of-line marker per language family. */
function commentMarker(lang: string): string {
  return lang === 'python' || lang === 'bash' ? '#' : '//';
}

const KEYWORDS: Record<string, readonly string[]> = {
  js: ['const', 'let', 'var', 'function', 'return', 'if', 'else', 'for', 'while', 'import', 'export', 'from', 'class', 'extends', 'new', 'await', 'async', 'try', 'catch', 'finally', 'throw', 'switch', 'case', 'break', 'continue', 'default', 'typeof', 'instanceof', 'in', 'of', 'yield', 'delete', 'void', 'static', 'get', 'set'],
  ts: ['const', 'let', 'var', 'function', 'return', 'if', 'else', 'for', 'while', 'import', 'export', 'from', 'class', 'extends', 'implements', 'interface', 'type', 'enum', 'new', 'await', 'async', 'try', 'catch', 'finally', 'throw', 'switch', 'case', 'break', 'continue', 'default', 'typeof', 'instanceof', 'in', 'of', 'yield', 'delete', 'void', 'static', 'public', 'private', 'protected', 'readonly', 'as', 'satisfies', 'keyof', 'namespace', 'declare', 'abstract'],
  python: ['def', 'class', 'return', 'if', 'elif', 'else', 'for', 'while', 'import', 'from', 'as', 'try', 'except', 'finally', 'raise', 'with', 'lambda', 'yield', 'pass', 'break', 'continue', 'and', 'or', 'not', 'in', 'is', 'global', 'nonlocal', 'assert', 'del', 'async', 'await'],
  json: [],
  bash: ['if', 'then', 'else', 'elif', 'fi', 'for', 'while', 'do', 'done', 'case', 'esac', 'function', 'return', 'export', 'local', 'echo', 'exit', 'set', 'source', 'cd', 'rm', 'mkdir', 'cp', 'mv', 'npm', 'npx', 'git', 'node', 'python', 'pip'],
};

const TRUE_FALSE_NULL = new Set(['true', 'false', 'null', 'undefined', 'None', 'True', 'False', 'nil']);

/** Ink color names used for token classes. */
const COLORS = { keyword: 'magenta', string: 'green', number: 'yellow', comment: 'gray', literal: 'cyan' } as const;

/**
 * Highlight one line of code into styled segments.
 * The line's characters are preserved exactly (segment text concatenates to
 * the input), so callers can still hard-slice long lines by character.
 */
export function highlightLine(line: string, lang: string): MarkdownSegment[] {
  const l = normalize(lang);
  const plain: MarkdownSegment[] = [{ text: line, color: 'cyan' }];
  if (!HIGHLIGHTERS.has(l)) return plain;

  if (l === 'json') return highlightJson(line);
  return highlightGeneric(line, l);
}

function highlightJson(line: string): MarkdownSegment[] {
  const out: MarkdownSegment[] = [];
  const re = /("(?:[^"\\]|\\.)*")(\s*:)?|(\b-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?\b)|(\btrue\b|\bfalse\b|\bnull\b)/g;
  let last = 0;
  for (const m of line.matchAll(re)) {
    const index = m.index ?? 0;
    if (index > last) out.push({ text: line.slice(last, index), color: 'cyan' });
    if (m[1] !== undefined) out.push({ text: m[1], color: COLORS.string });
    if (m[2] !== undefined) out.push({ text: m[2], color: 'cyan' });
    if (m[3] !== undefined) out.push({ text: m[3], color: COLORS.number });
    if (m[4] !== undefined) out.push({ text: m[4], color: COLORS.literal });
    last = index + m[0].length;
  }
  if (last < line.length) out.push({ text: line.slice(last), color: 'cyan' });
  return out;
}

interface Token {
  text: string;
  color?: string;
}

/** Word/punct tokenization with strings, comments, and numbers. */
function highlightGeneric(line: string, lang: string): Token[] {
  const out: Token[] = [];
  const comment = commentMarker(lang);
  const keywords = new Set(KEYWORDS[lang] ?? []);
  const re = /("(?:[^"\\]|\\.)*"|'(?:[^'\\]|\\.)*'|`(?:[^`\\]|\\.)*`)|(#.*$|\/\/.*$)|(\b\d+(?:\.\d+)?\b)|([A-Za-z_$][A-Za-z0-9_$]*)|(\s+)|(.)/g;
  let last = 0;
  const push = (text: string, color?: string): void => { if (text !== '') out.push({ text, color }); };
  for (const m of line.matchAll(re)) {
    const index = m.index ?? 0;
    if (index > last) push(line.slice(last, index), 'cyan');
    if (m[1] !== undefined) push(m[0], COLORS.string);
    else if (m[2] !== undefined) push(m[0], COLORS.comment);
    else if (m[3] !== undefined) push(m[0], COLORS.number);
    else if (m[4] !== undefined) {
      if (keywords.has(m[0])) push(m[0], COLORS.keyword);
      else if (TRUE_FALSE_NULL.has(m[0])) push(m[0], COLORS.literal);
      else push(m[0], 'cyan');
    } else if (m[5] !== undefined) push(m[0]);
    else push(m[0], 'cyan');
    last = index + m[0].length;
  }
  if (last < line.length) push(line.slice(last), 'cyan');
  return out;
}

/**
 * Highlight a whole code fence body into per-line segment arrays,
 * hard-slicing lines wider than `width` (segments split by character).
 */
export function highlightCode(code: readonly string[], lang: string | undefined, width: number): MarkdownSegment[][] {
  const l = lang !== undefined ? normalize(lang) : undefined;
  const can = l !== undefined && HIGHLIGHTERS.has(l);
  const out: MarkdownSegment[][] = [];
  for (const line of code) {
    let segments: MarkdownSegment[] = can ? highlightGeneric(line, l) : [{ text: line, color: 'cyan' }];
    // Hard-slice overlong lines, splitting segment text by character.
    while (segments.reduce((n, s) => n + s.text.length, 0) > width && segments.length > 0) {
      const row: MarkdownSegment[] = [];
      let remaining = width;
      let i = 0;
      while (i < segments.length && remaining > 0) {
        const seg = segments[i];
        if (seg.text.length <= remaining) { row.push(seg); remaining -= seg.text.length; i += 1; }
        else { row.push({ ...seg, text: seg.text.slice(0, remaining) }); segments[i] = { ...seg, text: seg.text.slice(remaining) }; remaining = 0; }
      }
      segments = segments.slice(i);
      out.push(row);
    }
    out.push(segments.length > 0 ? segments : [{ text: '', color: 'cyan' }]);
  }
  return out;
}
