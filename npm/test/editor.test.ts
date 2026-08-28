import assert from 'node:assert/strict';
import { test } from 'node:test';
import {
  deleteAt,
  deleteBefore,
  insertAt,
  layoutEditor,
  lineEndAt,
  lineStartAt,
  moveHorizontal,
  moveVerticalWrapped,
  normalizePaste,
} from '../src/ui/editor.js';

test('normalizePaste converts CRLF and CR to LF, leaving LF alone', () => {
  assert.equal(normalizePaste('a\r\nb\r\nc'), 'a\nb\nc');
  assert.equal(normalizePaste('a\rb\rc'), 'a\nb\nc');
  assert.equal(normalizePaste('a\r\nb\rc\nd'), 'a\nb\nc\nd');
  assert.equal(normalizePaste('\r\n\r'), '\n\n');
  assert.equal(normalizePaste('already\nlf'), 'already\nlf');
});

test('insertAt inserts at the cursor and advances it', () => {
  assert.deepEqual(insertAt('abc', 1, 'X'), { value: 'aXbc', cursor: 2 });
  assert.deepEqual(insertAt('abc', 0, 'X'), { value: 'Xabc', cursor: 1 });
  assert.deepEqual(insertAt('abc', 3, 'X'), { value: 'abcX', cursor: 4 });
  assert.deepEqual(insertAt('abc', 99, 'X'), { value: 'abcX', cursor: 4 }); // clamped
  assert.deepEqual(insertAt('', 0, 'hi'), { value: 'hi', cursor: 2 });
});

test('deleteBefore removes the char before the cursor, no-op at start', () => {
  assert.deepEqual(deleteBefore('abc', 2), { value: 'ac', cursor: 1 });
  assert.deepEqual(deleteBefore('abc', 0), { value: 'abc', cursor: 0 });
  assert.deepEqual(deleteBefore('ab\ncd', 3), { value: 'abcd', cursor: 2 }); // joins lines
});

test('deleteAt removes the char at the cursor, no-op at end', () => {
  assert.deepEqual(deleteAt('abc', 1), { value: 'ac', cursor: 1 });
  assert.deepEqual(deleteAt('abc', 3), { value: 'abc', cursor: 3 });
  assert.deepEqual(deleteAt('ab\ncd', 2), { value: 'abcd', cursor: 2 }); // joins lines
});

test('moveHorizontal clamps at the bounds', () => {
  assert.equal(moveHorizontal('abc', 1, -1), 0);
  assert.equal(moveHorizontal('abc', 0, -1), 0);
  assert.equal(moveHorizontal('abc', 2, 1), 3);
  assert.equal(moveHorizontal('abc', 3, 1), 3);
});

test('lineStartAt and lineEndAt find logical line edges', () => {
  assert.equal(lineStartAt('ab\ncd', 3), 3);
  assert.equal(lineStartAt('ab\ncd', 2), 0);
  assert.equal(lineStartAt('ab\ncd', 5), 3);
  assert.equal(lineEndAt('ab\ncd', 0), 2);
  assert.equal(lineEndAt('ab\ncd', 2), 2);
  assert.equal(lineEndAt('ab\ncd', 3), 5);
});

test('moveVerticalWrapped moves between wrapped rows at the same column', () => {
  // 'abcdef' wraps to rows 'abc' | 'def' at width 3
  assert.equal(moveVerticalWrapped('abcdef', 1, 1, 3), 4); // row 0 col 1 -> row 1 col 1
  assert.equal(moveVerticalWrapped('abcdef', 4, -1, 3), 1);
  assert.equal(moveVerticalWrapped('abcdef', 5, -1, 3), 2);
  assert.equal(moveVerticalWrapped('abcdef', 0, -1, 3), 0); // clamped at top
  assert.equal(moveVerticalWrapped('abcdef', 5, 1, 3), 5); // clamped at bottom
});

test('moveVerticalWrapped clamps the column to a shorter target row', () => {
  // rows: 'abc' | 'def' | 'x'
  assert.equal(moveVerticalWrapped('abcdef\nx', 5, 1, 3), 8); // col 2 -> end of 'x'
  assert.equal(moveVerticalWrapped('abcdef\nx', 8, -1, 3), 4); // col 1 -> row 'def' col 1
});

test('layoutEditor places the caret and wraps rows consistently', () => {
  assert.deepEqual(layoutEditor('abc', 1, 3), { lines: ['a▍bc'], cursorLine: 0, cursorCol: 1 });
  assert.deepEqual(layoutEditor('abc', 0, 3), { lines: ['▍abc'], cursorLine: 0, cursorCol: 0 });
  assert.deepEqual(layoutEditor('abc', 3, 3), { lines: ['abc▍'], cursorLine: 0, cursorCol: 3 });
  assert.deepEqual(layoutEditor('', 0, 3), { lines: ['▍'], cursorLine: 0, cursorCol: 0 });
});

test('layoutEditor wraps at width with no phantom empty row on exact-width lines', () => {
  const exact = layoutEditor('abcdef', 3, 3);
  assert.deepEqual(exact.lines, ['abc', '▍def']); // caret at wrap boundary -> next row
  assert.deepEqual(exact, { lines: ['abc', '▍def'], cursorLine: 1, cursorCol: 0 });
  const end = layoutEditor('abcdef', 6, 3);
  assert.deepEqual(end, { lines: ['abc', 'def▍'], cursorLine: 1, cursorCol: 3 });
});

test('layoutEditor keeps logical lines separate and places the caret across newlines', () => {
  assert.deepEqual(layoutEditor('ab\ncd', 2, 3), { lines: ['ab▍', 'cd'], cursorLine: 0, cursorCol: 2 });
  assert.deepEqual(layoutEditor('ab\ncd', 3, 3), { lines: ['ab', '▍cd'], cursorLine: 1, cursorCol: 0 });
  assert.deepEqual(layoutEditor('ab\ncd', 5, 3), { lines: ['ab', 'cd▍'], cursorLine: 1, cursorCol: 2 });
});
