import assert from 'node:assert/strict';
import { test } from 'node:test';
import { foldToolGroups, type GroupableLine } from '../src/ui/groups.js';

const tool = (text: string): GroupableLine => ({ role: 'tool', text, toolName: 't' });
const user = (text: string): GroupableLine => ({ role: 'user', text });

test('runs of 3+ tool lines fold into one summary line', () => {
  const out = foldToolGroups([tool('a'), tool('b'), tool('c'), tool('d')], false);
  assert.equal(out.length, 1);
  assert.equal(out[0]!.group?.count, 4);
});

test('expanded pass-through keeps every line', () => {
  const lines = [tool('a'), tool('b'), tool('c')];
  assert.equal(foldToolGroups(lines, true).length, 3);
});

test('short runs (<3) never fold', () => {
  const lines = [tool('a'), tool('b')];
  assert.deepEqual(foldToolGroups(lines, false), lines);
});

test('protectFrom shields the active turn: history folds, the current turn stays open', () => {
  const lines = [
    tool('h1'), tool('h2'), tool('h3'),       // 0..2  settled history — folds
    user('do the thing'),                      // 3     last user turn
    tool('n1'), tool('n2'), tool('n3'),        // 4..6  active turn — protected
  ];
  const out = foldToolGroups(lines, false, 3);
  const groups = out.filter((line) => line.group !== undefined);
  assert.equal(groups.length, 1);
  assert.equal(groups[0]!.group?.count, 3);
  // The three post-user tool lines survive unfolded.
  assert.equal(out.filter((line) => line.role === 'tool' && line.group === undefined).length, 3);
});
