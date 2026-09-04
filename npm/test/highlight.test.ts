import assert from 'node:assert/strict';
import { test } from 'node:test';
import { highlightCode } from '../src/ui/highlight.js';

const colorOf = (line: { text: string; color?: string }[], text: string): string | undefined =>
  line.find((s) => s.text === text)?.color;

// json is in the highlightable set, and there is a dedicated JSON
// highlighter that colours keys, strings, numbers and literals. The renderer
// the app actually calls reached only the generic keyword highlighter, so
// every JSON block rendered as if the JSON highlighter did not exist.
test('highlights a JSON block with the JSON highlighter', () => {
  const [line] = highlightCode(['{"name": "omniharness", "port": 20128, "on": true}'], 'json', 200);
  assert.ok(line, 'expected a highlighted line');
  assert.equal(colorOf(line, '"name"'), 'green', 'JSON keys/strings should use the string colour');
  assert.equal(colorOf(line, '20128'), 'yellow', 'JSON numbers should use the number colour');
  assert.equal(colorOf(line, 'true'), 'cyan', 'JSON literals should use the literal colour');
});

// The characters must survive highlighting exactly, or the hard-slicing that
// wraps long lines would corrupt the output.
test('preserves the JSON line exactly', () => {
  const src = '{"a": 1, "b": [true, null]}';
  const [line] = highlightCode([src], 'json', 200);
  assert.equal(line?.map((s) => s.text).join(''), src);
});

test('still highlights a non-JSON language generically', () => {
  const [line] = highlightCode(['const x = 1;'], 'ts', 200);
  assert.equal(colorOf(line ?? [], 'const'), 'magenta', 'keywords should use the keyword colour');
});

test('leaves an unknown language unstyled', () => {
  const [line] = highlightCode(['whatever this is'], 'brainfuck', 200);
  assert.deepEqual(line, [{ text: 'whatever this is', color: 'cyan' }]);
});
