import assert from 'node:assert/strict';
import { test } from 'node:test';
import { renderMarkdown, type MarkdownSegment } from '../src/ui/markdown.js';

const textOf = (lines: MarkdownSegment[][]): string => lines.map((line) => line.map((s) => s.text).join('')).join('\n');

test('wraps plain paragraphs to the width', () => {
  const out = renderMarkdown('one two three four five', 10);
  assert.deepEqual(textOf(out).split('\n'), ['one two', 'three four', 'five']);
});

test('hard-breaks words longer than the width', () => {
  const out = renderMarkdown('abcdefghij', 4);
  assert.deepEqual(textOf(out).split('\n'), ['abcd', 'efgh', 'ij']);
});

test('renders bold and italic', () => {
  const out = renderMarkdown('**bold** and *italic*', 40);
  assert.equal(out[0]?.[0]?.text, 'bold');
  assert.equal(out[0]?.[0]?.bold, true);
  assert.deepEqual(out[0]?.filter((s) => s.text === 'italic'), [{ text: 'italic', italic: true }]);
});

test('renders inline code and links', () => {
  const out = renderMarkdown('run `npm test` see [docs](https://x.dev)', 60);
  assert.match(textOf(out), /npm test/);
  assert.ok(out[0]?.some((s) => s.text === 'npm' && s.color === 'cyan'));
  assert.deepEqual(out[0]?.filter((s) => s.text === 'docs'), [{ text: 'docs', underline: true, color: 'blue' }]);
});

test('renders headings bold and cyan', () => {
  const out = renderMarkdown('# Title', 40);
  assert.deepEqual(out[0], [{ text: 'Title', bold: true, color: 'cyan' }]);
});

test('renders lists with bullets and keeps ordered numbers', () => {
  const bullets = renderMarkdown('- one\n- two', 40);
  assert.deepEqual(textOf(bullets), '• one\n• two');
  const ordered = renderMarkdown('1. first\n2. second', 40);
  assert.deepEqual(textOf(ordered), '1. first\n2. second');
});

test('renders blockquotes dimmed', () => {
  const out = renderMarkdown('> note', 40);
  assert.deepEqual(textOf(out), '│ note');
  assert.ok(out[0]?.some((s) => s.text === 'note' && s.dim === true));
});

test('renders horizontal rules', () => {
  const out = renderMarkdown('---', 8);
  assert.deepEqual(out, [[{ text: '────────', dim: true }]]);
});

test('ends a paragraph at a horizontal rule line', () => {
  const out = renderMarkdown('some text\n---', 40);
  assert.deepEqual(textOf(out), 'some text\n' + '─'.repeat(40));
});

test('renders fenced code blocks verbatim without word wrap', () => {
  const out = renderMarkdown('```\nconst x = 1;\n```', 40);
  assert.deepEqual(textOf(out), 'const x = 1;');
  assert.ok(out[0]?.every((s) => s.color === 'cyan'));
});

test('treats unclosed fences as code to the end', () => {
  const out = renderMarkdown('```\nconst x = 1;', 40);
  assert.deepEqual(textOf(out), 'const x = 1;');
});

test('ignores plain asterisks that are not emphasis', () => {
  const out = renderMarkdown('2 * 3 = 6', 40);
  assert.deepEqual(textOf(out), '2 * 3 = 6');
});

test('renders strikethrough', () => {
  const out = renderMarkdown('~~gone~~ stays', 40);
  assert.deepEqual(out[0]?.filter((s) => s.text === 'gone'), [{ text: 'gone', strikethrough: true }]);
});

test('renders a table with aligned columns and a bold header', () => {
  const out = renderMarkdown(['| left | right | center |', '| :--- | ---: | :---: |', '| a | bb | ccc |'].join('\n'), 60);
  assert.deepEqual(textOf(out), ['│ left │ right │ center │', '├──────┼───────┼────────┤', '│ a    │    bb │  ccc   │'].join('\n'));
  assert.ok(out[0]?.every((s) => s.bold === true && s.color === 'cyan'));
});

test('wraps table cells to the width and keeps columns aligned', () => {
  const out = renderMarkdown(['| one two three four | x |', '| --- | --- |', '| alpha beta gamma | y |'].join('\n'), 18);
  const lines = textOf(out).split('\n');
  assert.ok(lines.every((l) => l.length === lines[0]?.length));
  assert.match(textOf(out), /one two/);
  assert.match(textOf(out), /three/);
});

test('does not treat prose with a stray pipe as a table', () => {
  const out = renderMarkdown('speak | softly', 40);
  assert.deepEqual(textOf(out), 'speak | softly');
  assert.ok(out[0]?.every((s) => s.bold !== true));
});

test('does not treat a row without a matching delimiter as a table', () => {
  const out = renderMarkdown('a | b\nc | d', 40);
  assert.deepEqual(textOf(out), 'a | b c | d');
});

test('does not treat a delimiter with a different column count as a table', () => {
  const out = renderMarkdown('a | b | c\n--- | ---', 40);
  assert.deepEqual(textOf(out), 'a | b | c --- | ---');
});

test('renders inline markdown inside table cells', () => {
  const out = renderMarkdown('| **bold** | `code` |\n| --- | --- |', 40);
  assert.ok(out[0]?.some((s) => s.text === 'bold' && s.bold === true));
  assert.ok(out[0]?.some((s) => s.text === 'code' && s.color === 'cyan'));
});

test('renders unchecked and checked task checkboxes with distinct styles', () => {
  const out = renderMarkdown('- [ ] open\n- [x] done', 40);
  assert.deepEqual(textOf(out), '• [ ] open\n• [x] done');
  assert.deepEqual(out[0]?.filter((s) => s.text === '[ ]'), [{ text: '[ ]', dim: true }]);
  assert.deepEqual(out[1]?.filter((s) => s.text === '[x]'), [{ text: '[x]', color: 'green' }]);
});

test('renders ordered task checkboxes', () => {
  const out = renderMarkdown('1. [ ] first', 40);
  assert.deepEqual(textOf(out), '1. [ ] first');
});

test('renders two-level nested lists with indentation and per-level bullets', () => {
  const out = renderMarkdown('- top\n  - sub one\n  - sub two', 40);
  assert.deepEqual(textOf(out), '• top\n  ◦ sub one\n  ◦ sub two');
});

test('renders mixed ordered and unordered nesting', () => {
  const out = renderMarkdown('- top\n  1. first\n  2. second', 40);
  assert.deepEqual(textOf(out), '• top\n  1. first\n  2. second');
});

test('keeps a hyphen without a following space as prose', () => {
  const out = renderMarkdown('-not-a-list', 40);
  assert.deepEqual(textOf(out), '-not-a-list');
});

test('clamps deep ragged indentation to a flat depth', () => {
  const out = renderMarkdown('        - deep\n  - shallow', 40);
  assert.deepEqual(textOf(out), '    ▪ deep\n  ◦ shallow');
});
