import assert from 'node:assert/strict';
import { test } from 'node:test';
import { PassThrough, Writable } from 'node:stream';
import React from 'react';
import { render } from 'ink';

import { TerminalInterface } from '../src/ui/terminalInterface.js';
import { debounceResizeEvents } from '../src/ui/resizeDebounce.js';
import type { MastraEngine } from '../src/agent/mastraEngine.js';
import { asStdin, asStdout } from './streams.js';

class FakeStdin extends PassThrough {
  isTTY = true;
  setRawMode(): void {}
  ref(): void {}
  unref(): void {}
}

/** Counts writes as a proxy for redraws — each Ink frame is one write. */
class CountingStdout extends Writable {
  columns = 120;
  rows = 40;
  writes = 0;
  _write(_chunk: Buffer | string, _e: BufferEncoding, cb: (e?: Error | null) => void): void {
    this.writes += 1;
    cb();
  }
}

const sleep = (ms: number): Promise<void> => new Promise((resolve) => setTimeout(resolve, ms));

function fakeEngine(): MastraEngine {
  return {
    client: { endpoint: 'http://localhost:20128', snapshotMetrics: () => ({ compression: { inputTokens: 0, compressedTokens: 0, ratio: 1, strategy: 'none' }, fallback: { attempts: 0 } }) },
    state: { activeModel: 'auto/best-coding', workspace: { root: '/w' }, messages: [], taskQueue: [], mode: 'build', permissionMode: 'ask' },
    skills: [], mcpTools: [],
    subscribe: () => () => {},
    setApprovalHandler: () => {},
    selectModel: async () => {},
    attach: async () => [],
    run: async () => ({ content: '', model: '' }),
    runSwarm: async () => {},
    cancel: () => {},
    clearHistory: async () => {},
    stop: () => {},
    tools: {},
  } as unknown as MastraEngine;
}

// Reproduces what a Windows Terminal maximise-then-restore actually sends: not
// one resize event but a burst of intermediate sizes a few milliseconds apart,
// as the window animates through the transition. Reacting to each one is what
// left duplicated prompt boxes and orphaned frame fragments on screen — Ink
// redrew mid-animation against a size that was already stale by the time the
// escape sequences reached the terminal.
test('a burst of resize events during a maximise/restore produces one redraw, not one per event', async () => {
  const stdin = new FakeStdin();
  // This is what cli.tsx does before handing stdout to render(): the wrapper
  // has to be the thing Ink itself subscribes to, or its own internal
  // listener still redraws once per raw event regardless of anything this
  // test — or the component — does.
  const stdout = debounceResizeEvents(asStdout(new CountingStdout()), 80);
  const counting = stdout as unknown as CountingStdout;
  const app = render(React.createElement(TerminalInterface, { engine: fakeEngine() }), {
    stdin: asStdin(stdin), stdout, stderr: asStdout(new CountingStdout()), exitOnCtrlC: false,
  });
  await sleep(100); // let the initial render, boot sequence and probes settle

  const before = counting.writes;

  // The burst: columns and rows both moving, a few milliseconds apart, the
  // way an animated snap actually arrives — not a single clean jump. The gap
  // between events matters less than staying inside the 80ms debounce window
  // for the whole burst.
  const sizes: Array<[number, number]> = [
    [120, 40], [110, 38], [95, 34], [80, 30], [70, 26], [80, 30],
  ];
  for (const [columns, rows] of sizes) {
    counting.columns = columns;
    counting.rows = rows;
    counting.emit('resize');
    await sleep(15);
  }

  const duringBurst = counting.writes - before;

  // Now let it settle past the debounce window.
  await sleep(150);
  const afterSettle = counting.writes - before;

  app.unmount();

  assert.ok(
    duringBurst <= 1,
    `the burst itself triggered ${duringBurst} writes; undebounced resize handling redraws once per event`,
  );
  assert.ok(
    afterSettle >= 1,
    'the settled size must still produce a redraw — debouncing must not swallow the resize outright',
  );
});
