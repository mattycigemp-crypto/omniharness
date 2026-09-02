import assert from 'node:assert/strict';
import { test } from 'node:test';

import { statusMarker, STATUS_WIDTH, toolHead } from '../src/ui/toolrow.js';

test('every status marker is the same width', () => {
  // 'ok' and '..' were two columns and 'FAIL' four, so a failed call pushed
  // its own description two columns right of every row around it.
  const widths = new Set((['running', 'done', 'error', 'denied'] as const).map((s) => statusMarker(s).length));
  assert.deepEqual([...widths], [STATUS_WIDTH], `markers have widths ${[...widths].join(', ')}`);
});

test('every marker ends in a gap, so it cannot butt against the head', () => {
  // 'FAIL' is exactly as wide as a four-column field, so padding to four left
  // no separator at all: "FAIL$ go test ./..." ran together on screen.
  for (const status of ['running', 'done', 'error', 'denied'] as const) {
    assert.ok(statusMarker(status).endsWith(' '), `${status} marker has no trailing gap`);
  }
});

test('markers are words, not glyphs', () => {
  for (const status of ['running', 'done', 'error', 'denied'] as const) {
    assert.match(statusMarker(status), /^[a-zA-Z.]+ *$/, `${status} marker should be plain text`);
  }
  assert.ok(statusMarker('error').startsWith('FAIL'));
  assert.ok(statusMarker('done').startsWith('ok'));
});

test('the head is clipped to the room left beside the marker', () => {
  const width = 40;
  const head = toolHead('read', '/a/very/long/path/that/keeps/going/on/and/on.ts', width);
  assert.ok(head.length <= width - STATUS_WIDTH - 2, `head is ${head.length} columns in a ${width}-column row`);
  assert.ok(head.endsWith('…'));
  assert.ok(head.startsWith('read '));
});

test('a short head is left alone and a missing target is not padded', () => {
  assert.equal(toolHead('diff', '', 40), 'diff');
  assert.equal(toolHead('read', 'a.ts', 40), 'read a.ts');
});

test('a newline in a target cannot turn one row into two', () => {
  assert.equal(toolHead('$', 'ls -la\nrm -rf /', 60), '$ ls -la rm -rf /');
});

test('the head never goes negative on a tiny terminal', () => {
  for (const width of [0, 4, 8, 12]) {
    const head = toolHead('read', 'some/file.ts', width);
    assert.ok(head.length > 0, `width ${width} produced nothing`);
    assert.ok(head.length <= 12, `width ${width} produced ${head.length} columns`);
  }
});

test('the head leaves room for whatever follows it on the row', () => {
  const width = 76;
  const hint = '   Ctrl+T collapse';
  const long = 'go test ./internal/tools/ -run TestWorkspaceRootBehindASymlinkStillWorks';
  const head = toolHead('$', long, width, hint.length);
  assert.ok(
    STATUS_WIDTH + head.length + hint.length <= width,
    `row is ${STATUS_WIDTH + head.length + hint.length} columns wide in ${width}: the hint would wrap`,
  );
  // Without the reservation it does not fit, which is the bug.
  const unreserved = toolHead('$', long, width);
  assert.ok(STATUS_WIDTH + unreserved.length + hint.length > width);
});
