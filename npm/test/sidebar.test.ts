import assert from 'node:assert/strict';
import { test } from 'node:test';

import {
  clip, compactTokens, conversationWidth, formatCost, overflowCount,
  SIDEBAR_MIN_SPLIT, SIDEBAR_WIDTH, sidebarMode, todoRows, usageRows,
} from '../src/ui/sidebar.js';

test('the sidebar splits when there is room and replaces when there is not', () => {
  assert.equal(sidebarMode(120, true), 'split');
  assert.equal(sidebarMode(SIDEBAR_MIN_SPLIT, true), 'split');
  // Ink cannot overlay a panel on the conversation the way OpenCode does, so
  // below the split width it takes the screen rather than squeezing both.
  assert.equal(sidebarMode(SIDEBAR_MIN_SPLIT - 1, true), 'replace');
  assert.equal(sidebarMode(60, true), 'replace');
  assert.equal(sidebarMode(200, false), 'hidden');
});

test('the conversation keeps a usable width beside the sidebar', () => {
  assert.equal(conversationWidth(120, 'hidden'), 120);
  assert.equal(conversationWidth(120, 'replace'), 120);
  assert.equal(conversationWidth(120, 'split'), 120 - SIDEBAR_WIDTH - 1);
  // Never negative, however narrow the terminal gets.
  assert.ok(conversationWidth(30, 'split') >= 20);
});

test('compactTokens stays exact while it is readable', () => {
  assert.equal(compactTokens(0), '0');
  assert.equal(compactTokens(999), '999');
  assert.equal(compactTokens(1000), '1.0k');
  assert.equal(compactTokens(9999), '10.0k');
  assert.equal(compactTokens(12_400), '12k');
  assert.equal(compactTokens(1_500_000), '1.5M');
  assert.equal(compactTokens(-5), '0');
  assert.equal(compactTokens(Number.NaN), '0');
});

test('sub-cent spend is not reported as free', () => {
  // A turn costing a third of a cent is the normal case. Rounding it to $0.00
  // would tell the user every run is free.
  assert.equal(formatCost(0.0034), '$0.0034');
  assert.equal(formatCost(0.5), '$0.50');
  assert.equal(formatCost(12.3456), '$12.35');
  assert.equal(formatCost(0), '$0');
  assert.equal(formatCost(Number.NaN), '$0');
});

test('usage rows are absent until something has been measured', () => {
  assert.deepEqual(usageRows({}), []);
  assert.deepEqual(usageRows({ tokensIn: 0, tokensOut: 0, costUSD: 0 }), []);

  const full = usageRows({ tokensIn: 1200, tokensOut: 340, costUSD: 0.0034, requests: 3 });
  assert.deepEqual(full.map((r) => r.label), ['tokens', 'cost', 'calls']);
  assert.equal(full[0]?.value, '1.2k in · 340 out');
  assert.equal(full[1]?.value, '$0.0034');
});

test('a figure nobody measured is not reported as zero', () => {
  // The TypeScript client tracks context and request count; output tokens and
  // spend live on the Go side. "1.2k in · 0 out" would claim the model
  // returned nothing, and "$0" would claim the run was free.
  const rows = usageRows({ tokensIn: 1200, requests: 3 });
  assert.deepEqual(rows.map((r) => r.label), ['tokens', 'calls']);
  assert.equal(rows[0]?.value, '1.2k in', 'must not invent an output count');
  assert.ok(!rows.some((r) => r.label === 'cost'), 'must not invent a cost');
});

test('todo rows mark state in words, not colour alone', () => {
  const rows = todoRows([
    { title: 'read the file', status: 'done' },
    { title: 'fix the bug', status: 'active' },
    { title: 'run the tests', status: 'pending' },
  ], 5, 20);
  assert.deepEqual(rows.map((r) => r.marker), ['x', '>', '-']);
  assert.equal(rows[1]?.active, true);
  assert.equal(rows[0]?.active, false);
});

test('todo rows are capped and clipped to one line each', () => {
  const many = Array.from({ length: 20 }, (_, i) => ({ title: `task number ${i}`, status: 'pending' }));
  const rows = todoRows(many, 4, 12);
  assert.equal(rows.length, 4);
  for (const row of rows) assert.ok([...row.title].length <= 12, `"${row.title}" is too wide`);
  assert.equal(overflowCount(20, 4), 16);
  assert.equal(overflowCount(3, 4), 0);
});

test('clip collapses newlines so a row cannot become two', () => {
  assert.equal(clip('two\nlines here', 40), 'two lines here');
  assert.equal([...clip('日本語日本語日本語', 5)].length, 5, 'clip counts characters, not bytes');
});
