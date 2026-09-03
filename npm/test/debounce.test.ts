import assert from 'node:assert/strict';
import { test } from 'node:test';

import { debounce } from '../src/ui/debounce.js';

const sleep = (ms: number): Promise<void> => new Promise((resolve) => setTimeout(resolve, ms));

test('a burst of calls produces exactly one, after the burst ends', async () => {
  // This is the resize-storm case: Windows Terminal fires several resize
  // events a few milliseconds apart while a maximise/restore animates. Each
  // one reacting on its own is what produced the duplicated frames.
  let calls = 0;
  const d = debounce(() => { calls += 1; }, 30);

  for (let i = 0; i < 8; i++) {
    d();
    await sleep(5); // well inside the debounce window
  }
  assert.equal(calls, 0, 'must not have fired while the burst was still arriving');

  await sleep(50);
  assert.equal(calls, 1, `fired ${calls} times, want exactly one call for the whole burst`);
});

test('only the last call in the burst is the one that runs', async () => {
  const seen: number[] = [];
  const d = debounce((n: number) => seen.push(n), 20);

  d(1); d(2); d(3);
  await sleep(50);

  assert.deepEqual(seen, [3], 'an intermediate size must never reach the caller');
});

test('calls spaced further apart than the delay each fire on their own', async () => {
  let calls = 0;
  const d = debounce(() => { calls += 1; }, 20);

  d();
  await sleep(40);
  d();
  await sleep(40);

  assert.equal(calls, 2, 'a deliberate, unhurried resize must not be swallowed');
});

test('cancel drops a pending call', async () => {
  // The case a missing cancel breaks: a resize arrives just before the
  // component unmounts, and the debounced setState fires afterwards anyway.
  let calls = 0;
  const d = debounce(() => { calls += 1; }, 20);

  d();
  d.cancel();
  await sleep(40);

  assert.equal(calls, 0, 'a cancelled call must never run');
});

test('calling again after cancel schedules a fresh call', async () => {
  let calls = 0;
  const d = debounce(() => { calls += 1; }, 20);

  d();
  d.cancel();
  d();
  await sleep(40);

  assert.equal(calls, 1, 'cancel must not permanently disable the debounced function');
});
