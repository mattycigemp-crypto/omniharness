import assert from 'node:assert/strict';
import { test } from 'node:test';

import { capabilityLine, recentRows, relativeTime, shortenPath, twoColumn, TWO_COLUMN_MIN_WIDTH } from '../src/ui/home.js';

const NOW = Date.parse('2026-09-02T12:00:00Z');
const ago = (ms: number): string => new Date(NOW - ms).toISOString();

test('relativeTime picks one unit and reads at a glance', () => {
  const cases: Array<[number, string]> = [
    [0, 'now'],
    [30_000, 'now'],
    [60_000, '1m ago'],
    [59 * 60_000, '59m ago'],
    [60 * 60_000, '1h ago'],
    [23 * 3_600_000, '23h ago'],
    [24 * 3_600_000, '1d ago'],
    [6 * 86_400_000, '6d ago'],
    [7 * 86_400_000, '1w ago'],
    [400 * 86_400_000, '1y ago'],
  ];
  for (const [elapsed, want] of cases) {
    assert.equal(relativeTime(ago(elapsed), NOW), want, `${elapsed}ms should read ${want}`);
  }
});

test('a session saved seconds ago is not "0m ago"', () => {
  // Zero of anything on a home screen reads as a bug rather than as freshness.
  assert.equal(relativeTime(ago(1000), NOW), 'now');
});

test('an unparseable timestamp renders as nothing, not NaN', () => {
  for (const bad of ['', 'not a date', 'undefined']) {
    assert.equal(relativeTime(bad, NOW), '');
  }
});

test('a clock skewed into the future does not produce a negative age', () => {
  assert.equal(relativeTime(new Date(NOW + 60_000).toISOString(), NOW), 'now');
});

test('recentRows caps the list and clips long names', () => {
  const sessions = Array.from({ length: 10 }, (_, i) => ({ name: `session-${i}`, savedAt: ago(i * 3_600_000) }));
  const rows = recentRows(sessions, 4, 20, NOW);
  assert.equal(rows.length, 4, 'the block has to stay a predictable height');
  assert.deepEqual(rows.map((r) => r.name), ['session-0', 'session-1', 'session-2', 'session-3']);
  assert.equal(rows[1]?.age, '1h ago');

  const long = recentRows([{ name: 'a-really-quite-long-session-name', savedAt: ago(0) }], 4, 12, NOW);
  assert.equal(long[0]?.name.length, 12, 'a name must not wrap the row onto a second line');
  assert.ok(long[0]?.name.endsWith('…'));
});

test('recentRows handles an empty list and a zero limit', () => {
  assert.deepEqual(recentRows([], 4, 20, NOW), []);
  assert.deepEqual(recentRows([{ name: 'x', savedAt: ago(0) }], 0, 20, NOW), []);
});

test('the home screen stacks rather than squeezing on a narrow terminal', () => {
  assert.equal(twoColumn(TWO_COLUMN_MIN_WIDTH), true);
  assert.equal(twoColumn(TWO_COLUMN_MIN_WIDTH - 1), false);
  assert.equal(twoColumn(40), false);
});

test('shortenPath keeps the end, which is the part that identifies a project', () => {
  const p = '/Users/someone/code/work/omniharness';
  assert.equal(shortenPath(p, 100), p);
  const short = shortenPath(p, 20);
  assert.equal(short.length, 20);
  assert.ok(short.startsWith('…'));
  assert.ok(short.endsWith('omniharness'), `kept the wrong end: ${short}`);
});

test('capabilityLine says nothing when there is nothing to say', () => {
  assert.equal(capabilityLine(0, 0, 0), '');
  assert.equal(capabilityLine(3, 0, 0), '3 skills');
  assert.equal(capabilityLine(1, 0, 0), '1 skill');
  assert.equal(capabilityLine(63, 41, 0), '63 skills from 41 plugins');
  assert.equal(capabilityLine(1, 1, 0), '1 skill from 1 plugin');
  assert.equal(capabilityLine(0, 0, 7), '7 mcp tools');
  assert.equal(capabilityLine(2, 1, 1), '2 skills from 1 plugin · 1 mcp tool');
});
