import assert from 'node:assert/strict';
import { test } from 'node:test';
import { PassThrough, Writable } from 'node:stream';
import React from 'react';
import { render } from 'ink';

import { TerminalInterface } from '../src/ui/terminalInterface.js';
import type { MastraEngine } from '../src/agent/mastraEngine.js';
import { asStdin, asStdout } from './streams.js';

class FakeStdin extends PassThrough {
  isTTY = true;
  setRawMode(): void {}
  ref(): void {}
  unref(): void {}
}

class FakeStdout extends Writable {
  columns = 120;
  rows = 40;
  output = '';
  _write(chunk: Buffer | string, _e: BufferEncoding, cb: (e?: Error | null) => void): void {
    this.output += chunk.toString();
    cb();
  }
}

const sleep = (ms: number): Promise<void> => new Promise((resolve) => setTimeout(resolve, ms));
const CTRL_B = String.fromCharCode(2);

function fakeEngine(): MastraEngine {
  return {
    client: {
      endpoint: 'http://localhost:20128',
      snapshotMetrics: () => ({
        compression: { inputTokens: 12_400, compressedTokens: 9000, ratio: 0.72, strategy: 'window' },
        fallback: { attempts: 0 },
        requestCount: 3,
      }),
    },
    state: {
      activeModel: 'auto/best-coding',
      workspace: { root: '/Users/someone/code/work/omniharness' },
      messages: [],
      taskQueue: [
        { id: 't1', title: 'read the failing test', status: 'done' },
        { id: 't2', title: 'fix the confinement check', status: 'active' },
      ],
      mode: 'build',
      permissionMode: 'ask',
    },
    skills: [],
    mcpTools: [],
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

/** Render, optionally toggle the panel, and return the final frame. */
async function screen(width: number, openPanel: boolean): Promise<string> {
  const stdin = new FakeStdin();
  const stdout = new FakeStdout();
  stdout.columns = width;
  const app = render(React.createElement(TerminalInterface, { engine: fakeEngine() }), {
    stdin: asStdin(stdin), stdout: asStdout(stdout), stderr: asStdout(new FakeStdout()), exitOnCtrlC: false,
  });
  await sleep(40);
  if (openPanel) {
    stdin.write(CTRL_B);
    await sleep(60);
  }
  app.unmount();
  const marker = `${String.fromCharCode(27)}[G`;
  const parts = stdout.output.split(marker);
  return (parts[parts.length - 1] ?? stdout.output)
    .replace(new RegExp(`${String.fromCharCode(27)}\\[[0-9;?]*[A-Za-z]`, 'g'), '');
}

const occurrences = (haystack: string, needle: string): number => haystack.split(needle).length - 1;

test('the panel is closed until it is asked for', async () => {
  const out = await screen(120, false);
  assert.ok(!out.includes('Ctrl+B hides this'), 'the panel must not open on its own');
});

// Got this wrong twice: first the queue showed in both columns in split mode,
// then again in replace mode after the gate was written for split alone. The
// same list on screen twice is the failure, whichever layout is in play.
for (const [width, layout] of [[120, 'split'], [80, 'replace']] as const) {
  test(`the task queue appears once in ${layout} layout`, async () => {
    const out = await screen(width, true);
    assert.ok(out.includes('Ctrl+B hides this'), `the panel did not open at ${width} columns`);
    assert.equal(
      occurrences(out, 'fix the confinement check'), 1,
      `the queue is rendered twice at ${width} columns:\n${out}`,
    );
  });
}

test('the panel reports only what the client measures', async () => {
  const out = await screen(120, true);
  assert.ok(out.includes('12k in'), 'context tokens are measured and should show');
  assert.ok(out.includes('calls'), 'request count is measured and should show');
  // The TypeScript client tracks neither output tokens nor spend; inventing
  // either would put a number on screen that nobody computed.
  assert.ok(!out.includes('out'), 'must not claim an output token count');
  assert.ok(!/\$\d/.test(out), 'must not claim a cost');
});

test('toggling twice returns to the conversation', async () => {
  const stdin = new FakeStdin();
  const stdout = new FakeStdout();
  stdout.columns = 120;
  const app = render(React.createElement(TerminalInterface, { engine: fakeEngine() }), {
    stdin: asStdin(stdin), stdout: asStdout(stdout), stderr: asStdout(new FakeStdout()), exitOnCtrlC: false,
  });
  await sleep(40);
  stdin.write(CTRL_B);
  await sleep(50);
  stdin.write(CTRL_B);
  await sleep(50);
  app.unmount();
  const marker = `${String.fromCharCode(27)}[G`;
  const parts = stdout.output.split(marker);
  const final = (parts[parts.length - 1] ?? '').replace(new RegExp(`${String.fromCharCode(27)}\\[[0-9;?]*[A-Za-z]`, 'g'), '');
  assert.ok(!final.includes('Ctrl+B hides this'), 'a second Ctrl+B must close it');
});
