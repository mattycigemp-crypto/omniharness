import assert from 'node:assert/strict';
import { test } from 'node:test';
import { diffSegments, looksLikeDiff, parseDiff } from '../src/ui/diff.js';

const SAMPLE =
  'diff --git a/src/a.ts b/src/a.ts\n'
  + 'index 0000001..0000002 100644\n'
  + '--- a/src/a.ts\n'
  + '+++ b/src/a.ts\n'
  + '@@ -10,7 +10,7 @@ export function f() {\n'
  + '  const x = readLine();\n'
  + '+  const y = x + 1;\n'
  + '-  const y = x;\n'
  + '  return y;\n';

test('detects unified-diff output', () => {
  assert.equal(looksLikeDiff(SAMPLE), true);
  assert.equal(looksLikeDiff('plain tool output\nwith no markers'), false);
  assert.equal(looksLikeDiff(''), false);
});

test('parses diff lines with kinds', () => {
  const lines = parseDiff(SAMPLE);
  assert.ok(lines.some((l) => l.kind === '+' && l.text === '  const y = x + 1;'));
  assert.ok(lines.some((l) => l.kind === '-' && l.text === '  const y = x;'));
  assert.ok(lines.some((l) => l.kind === '@'));
  assert.ok(lines.some((l) => l.kind === 'file'));
});

test('renders added/removed lines with green/red color and preserved indent', () => {
  const rows = diffSegments(SAMPLE, 80);
  const textOf = (row: readonly { text: string }[]): string => row.map((s) => s.text).join('');
  const added = rows.find((row) => textOf(row).includes('const y = x + 1'));
  assert.ok(added?.every((s) => s.color === 'green'));
  assert.match(textOf(added!), /\+ +const y = x \+ 1/);
  const removed = rows.find((row) => textOf(row).includes('const y = x;'));
  assert.ok(removed?.every((s) => s.color === 'red'));
});

test('plain text yields no diff rows (the UI gates on looksLikeDiff first)', () => {
  assert.deepEqual(diffSegments('just some text', 40), []);
});