import assert from 'node:assert/strict';
import { EventEmitter } from 'node:events';
import { test } from 'node:test';

import { debounceResizeEvents } from '../src/ui/resizeDebounce.js';

const sleep = (ms: number): Promise<void> => new Promise((resolve) => setTimeout(resolve, ms));

/** Enough of a TTY stream's interface for the wrapper to operate on. */
class FakeStream extends EventEmitter {
  columns = 120;
  rows = 40;
  writes: unknown[] = [];
  write(chunk: unknown): boolean {
    this.writes.push(chunk);
    return true;
  }
}

test('a burst of resize events reaches the listener once', async () => {
  const stream = debounceResizeEvents(new FakeStream() as unknown as NodeJS.WriteStream, 30);
  let calls = 0;
  stream.on('resize', () => { calls += 1; });

  for (let i = 0; i < 6; i++) {
    stream.emit('resize');
    await sleep(5);
  }
  assert.equal(calls, 0, 'must not fire while the burst is still arriving');

  await sleep(50);
  assert.equal(calls, 1, `listener called ${calls} times for one burst, want one`);
});

// This is the case the whole thing exists for. Ink calls stream.on('resize',
// this.resized) itself, inside its own constructor, with no hook for
// application code to intervene — so the fix has to work for a listener that
// was never written with debouncing in mind, attached by code this project
// does not control.
test('a listener that knows nothing about debouncing still only fires once per burst', async () => {
  const stream = debounceResizeEvents(new FakeStream() as unknown as NodeJS.WriteStream, 30);
  const seen: number[] = [];
  let n = 0;
  // Mimics Ink's own resized handler: no awareness this stream is wrapped.
  const inkStyleListener = (): void => { seen.push(n); };
  stream.on('resize', inkStyleListener);

  n = 1; stream.emit('resize');
  n = 2; stream.emit('resize');
  n = 3; stream.emit('resize');
  await sleep(50);

  assert.deepEqual(seen, [3], 'only the settled state should ever reach an unmodified listener');
});

test('off cancels a pending call, not just future ones', async () => {
  const stream = debounceResizeEvents(new FakeStream() as unknown as NodeJS.WriteStream, 30);
  let calls = 0;
  const listener = (): void => { calls += 1; };

  stream.on('resize', listener);
  stream.emit('resize');
  stream.off('resize', listener);
  await sleep(50);

  assert.equal(calls, 0, 'a listener removed mid-debounce must not fire later anyway');
});

test('two listeners on the same stream are debounced independently', async () => {
  // Ink and the application both call stream.on('resize', …) on the same
  // object. Neither should see the other's timer.
  const stream = debounceResizeEvents(new FakeStream() as unknown as NodeJS.WriteStream, 30);
  let a = 0;
  let b = 0;
  stream.on('resize', () => { a += 1; });
  stream.on('resize', () => { b += 1; });

  stream.emit('resize');
  stream.emit('resize');
  await sleep(50);

  assert.equal(a, 1);
  assert.equal(b, 1);
});

test('every other event passes through unaffected', async () => {
  const stream = debounceResizeEvents(new FakeStream() as unknown as NodeJS.WriteStream, 30);
  let calls = 0;
  stream.on('data', () => { calls += 1; });

  stream.emit('data');
  stream.emit('data');

  // Not debounced: no wait required.
  assert.equal(calls, 2, 'only resize should be coalesced');
});

test('write, columns and rows are untouched', () => {
  const raw = new FakeStream();
  const stream = debounceResizeEvents(raw as unknown as NodeJS.WriteStream, 30);
  stream.write('hello');
  assert.deepEqual(raw.writes, ['hello']);
  assert.equal(stream.columns, 120);
  assert.equal(stream.rows, 40);
});
