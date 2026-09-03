import assert from 'node:assert/strict';
import { test } from 'node:test';
import { PassThrough, Writable } from 'node:stream';
import React from 'react';
import { render } from 'ink';

import { TerminalInterface } from '../src/ui/terminalInterface.js';
import { SYNC_BEGIN, SYNC_END } from '../src/ui/termcaps.js';
import type { MastraEngine } from '../src/agent/mastraEngine.js';
import { asStdin, asStdout } from './streams.js';

class FakeStdin extends PassThrough {
  isTTY = true;
  setRawMode(): void {}
  ref(): void {}
  unref(): void {}
}

class CapturingStdout extends Writable {
  columns = 120;
  rows = 40;
  output = '';
  _write(chunk: Buffer | string, _e: BufferEncoding, cb: (e?: Error | null) => void): void {
    this.output += chunk.toString();
    cb();
  }
}

const sleep = (ms: number): Promise<void> => new Promise((resolve) => setTimeout(resolve, ms));

/**
 * Strip ANSI escape sequences from a captured frame, including the DECRQM
 * query/reply forms (`CSI ? Ps $ p`, `CSI ? Ps ; Pm $ y`) — the very thing
 * this file's tests send and check for the absence of. A plain
 * `ESC[…letter` pattern does not match those: the intermediate `$` byte
 * before the final letter is not itself a digit, `;` or `?`, so a stripper
 * that only expects those leaves the query bytes this project's own probes
 * write to the terminal sitting in the frame, un-stripped — and those
 * happen to contain the same digits ("2026") the tests are checking for,
 * which reads as a leak in the test even when the application is clean.
 */
function stripEscapes(text: string): string {
  return text.replace(new RegExp(`${String.fromCharCode(27)}\\[[0-9;?]*\\$?[A-Za-z]`, 'g'), '');
}

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

// Reproduces the second screenshot exactly: `[?2026;2$y` — the terminal's own
// reply to the synchronized-output query this project sends at startup —
// showing up typed into the input box after a resize.
//
// The probe listener that was supposed to claim this reply was torn down
// after a fixed 300ms shared with an unrelated kitty-protocol timeout. When a
// terminal answers this specific query later than that — which one evidently
// does — nothing is left listening for it, and the same bytes still reach
// Ink's own useInput regardless (event delivery on stdin is not exclusive),
// where an unrecognised multi-character value is inserted as pasted text.
test('a late synchronized-output reply does not get typed into the input box', async () => {
  const stdin = new FakeStdin();
  const stdout = new CapturingStdout();
  const app = render(React.createElement(TerminalInterface, { engine: fakeEngine() }), {
    stdin: asStdin(stdin), stdout: asStdout(stdout), stderr: asStdout(new CapturingStdout()), exitOnCtrlC: false,
  });

  // Past the kitty probe's 300ms window — the point at which, before this
  // fix, nothing was left listening for a sync-output reply.
  await sleep(350);

  // The literal bytes a terminal sends back for `CSI ? 2026 $p`.
  stdin.write('\x1b[?2026;2$y');
  await sleep(50);

  app.unmount();
  const marker = `${String.fromCharCode(27)}[G`;
  const parts = stdout.output.split(marker);
  const frame = stripEscapes(parts[parts.length - 1] ?? stdout.output);

  assert.ok(
    !frame.includes('2026') && !frame.includes('$y'),
    `the terminal's own reply was typed into the screen:\n${frame}`,
  );
});

// The same reply arriving inside the 300ms window must still work exactly as
// before — a fast terminal is the common case and must not regress.
test('a prompt synchronized-output reply is still recognised', async () => {
  const stdin = new FakeStdin();
  const stdout = new CapturingStdout();
  const app = render(React.createElement(TerminalInterface, { engine: fakeEngine() }), {
    stdin: asStdin(stdin), stdout: asStdout(stdout), stderr: asStdout(new CapturingStdout()), exitOnCtrlC: false,
  });

  await sleep(30);
  stdin.write('\x1b[?2026;2$y');
  await sleep(30);

  app.unmount();
  const marker = `${String.fromCharCode(27)}[G`;
  const parts = stdout.output.split(marker);
  const frame = stripEscapes(parts[parts.length - 1] ?? stdout.output);

  assert.ok(!frame.includes('2026') && !frame.includes('$y'), `leaked even on the fast path:\n${frame}`);
});

// A real paste that happens to look nothing like a reply must still work.
test('an ordinary paste still lands in the input box', async () => {
  const stdin = new FakeStdin();
  const stdout = new CapturingStdout();
  const app = render(React.createElement(TerminalInterface, { engine: fakeEngine() }), {
    stdin: asStdin(stdin), stdout: asStdout(stdout), stderr: asStdout(new CapturingStdout()), exitOnCtrlC: false,
  });

  await sleep(50);
  stdin.write('hello world');
  await sleep(30);

  app.unmount();
  const marker = `${String.fromCharCode(27)}[G`;
  const parts = stdout.output.split(marker);
  const frame = stripEscapes(parts[parts.length - 1] ?? stdout.output);

  assert.ok(frame.includes('hello world'), `an ordinary paste must still be inserted:\n${frame}`);
});

// Not appearing in the input box is only half of what "handled correctly"
// means. The sync-output probe used to be torn down after a fixed 300ms
// shared with the unrelated kitty-protocol timeout — a late reply had
// nowhere to go, so the feature it exists for (bracketing each redraw so the
// terminal composites it atomically instead of tearing mid-repaint) silently
// never turned on for the rest of the session. The isEncodedKey guard on its
// own stops the reply becoming visible text; it does nothing to fix that.
// This checks the actual, observable effect: once a late reply is answered,
// the next thing written to the stream must be wrapped in the synchronized-
// output markers.
test('a late reply still turns synchronized output on', async () => {
  const stdin = new FakeStdin();
  const stdout = new CapturingStdout();
  const app = render(React.createElement(TerminalInterface, { engine: fakeEngine() }), {
    stdin: asStdin(stdin), stdout: asStdout(stdout), stderr: asStdout(new CapturingStdout()), exitOnCtrlC: false,
  });

  await sleep(350); // past the kitty probe's 300ms window
  const before = stdout.output.length;
  stdin.write('\x1b[?2026;2$y');
  await sleep(30);

  // Trigger one more write so there is a frame to inspect after the reply.
  stdin.write('x');
  await sleep(30);
  app.unmount();

  const written = stdout.output.slice(before);
  assert.ok(
    written.includes(SYNC_BEGIN) && written.includes(SYNC_END),
    `a write after the late reply was not wrapped in synchronized-output markers, so the feature never turned on:\n${JSON.stringify(written)}`,
  );
});
