import assert from 'node:assert/strict';
import { test } from 'node:test';

import { minimumHeight, planViewport, type ViewportInput } from '../src/ui/viewport.js';

const base: ViewportInput = { rows: 40, editorLines: 1, toolCards: 5, todos: 6, wantHero: false };

/** Rough height of the frame the plan describes, for the fits-on-screen check. */
function frameHeight(input: ViewportInput): number {
  const plan = planViewport(input);
  return 6 + Math.max(1, input.editorLines)
    + (plan.showHero ? 12 : 0) + plan.liveLines + plan.toolCards + plan.todoRows;
}

test('a tall terminal shows everything', () => {
  const plan = planViewport({ ...base, wantHero: true });
  assert.equal(plan.toolCards, 5);
  assert.equal(plan.todoRows, 6);
  assert.equal(plan.showHero, true);
  assert.ok(plan.liveLines >= 10, `streaming got only ${plan.liveLines} lines on a 40-row terminal`);
});

// The failure this exists for: a frame taller than the viewport makes Ink's
// cursor-up redraw walk off the top, which is what garbles the display after
// a window is maximised and then floated back down.
for (const rows of [8, 10, 12, 14, 16, 20, 24, 30, 40, 60]) {
  test(`the frame fits a ${rows}-row terminal`, () => {
    for (const editorLines of [1, 3, 6]) {
      const input = { ...base, rows, editorLines, wantHero: true };
      // A terminal shorter than the input box plus its chrome cannot be
      // satisfied by giving anything up, so the frame is allowed to be its
      // irreducible minimum there and no larger.
      const ceiling = Math.max(rows, minimumHeight(editorLines));
      assert.ok(
        frameHeight(input) <= ceiling,
        `frame is ${frameHeight(input)} rows in a ${rows}-row terminal (editor ${editorLines}): ${JSON.stringify(planViewport(input))}`,
      );
    }
  });
}

test('the home screen is dropped before anything useful is', () => {
  // Half a home screen is worse than none, and it only appears before the
  // first turn anyway.
  assert.equal(planViewport({ ...base, rows: 40, wantHero: true }).showHero, true);
  assert.equal(planViewport({ ...base, rows: 14, wantHero: true }).showHero, false);
});

test('a running turn is never completely invisible', () => {
  for (const rows of [8, 9, 10, 12]) {
    const plan = planViewport({ ...base, rows, editorLines: 4 });
    assert.ok(plan.liveLines >= 2, `streaming got ${plan.liveLines} lines at ${rows} rows`);
  }
});

test('nothing goes negative on an absurd terminal', () => {
  const plan = planViewport({ rows: 1, editorLines: 20, toolCards: 9, todos: 9, wantHero: true });
  for (const [key, value] of Object.entries(plan)) {
    if (typeof value === 'number') assert.ok(value >= 0, `${key} = ${value}`);
  }
  assert.equal(plan.showHero, false);
});

test('a plan never promises more than it was given', () => {
  const plan = planViewport({ ...base, rows: 60, toolCards: 2, todos: 1 });
  assert.equal(plan.toolCards, 2, 'must not invent tool cards that do not exist');
  assert.equal(plan.todoRows, 1);
});

test('a zero tool-card budget means zero, not everything', () => {
  // The trap this pins is at the call site rather than here: `slice(-0)` is
  // `slice(0)`, which returns the whole array — the exact opposite of a
  // budget of none.
  const plan = planViewport({ rows: 10, editorLines: 6, toolCards: 9, todos: 9, wantHero: false });
  const cards = ['a', 'b', 'c'];
  const shown = plan.toolCards > 0 ? cards.slice(-plan.toolCards) : [];
  assert.equal(plan.toolCards, 0, 'a cramped terminal should have no room for tool cards');
  assert.deepEqual(shown, [], 'a zero budget must show nothing');
  assert.deepEqual(cards.slice(-plan.toolCards), cards, 'this is why the guard is needed');
});
