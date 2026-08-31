import assert from 'node:assert/strict';
import os from 'node:os';
import path from 'node:path';
import { promises as fs } from 'node:fs';
import { test } from 'node:test';
import { PassThrough, Writable } from 'node:stream';
import React from 'react';
import { render } from 'ink';
import { TerminalInterface } from '../src/ui/terminalInterface.js';
import type { MastraEngine } from '../src/agent/mastraEngine.js';

class FakeStdin extends PassThrough {
  isTTY = true;
  setRawMode(): void {}
  ref(): void {}
  unref(): void {}
}

class FakeStdout extends Writable {
  columns = 100;
  rows = 40;
  output = '';
  _write(chunk: Buffer | string, _encoding: BufferEncoding, callback: (error?: Error | null) => void): void {
    this.output += chunk.toString();
    callback();
  }
}

const sleep = (ms: number): Promise<void> => new Promise((resolve) => setTimeout(resolve, ms));

const stripAnsi = (text: string): string => text.replace(/\x1b\[[0-9;?]*[A-Za-z]/g, '').replace(/\x1b[()][0-9A-Z]/g, '');

function makeEngine(): MastraEngine {
  return {
    client: {
      listCombos: async () => [],
      snapshotMetrics: () => ({
        compression: { inputTokens: 0, compressedTokens: 0, ratio: 1, strategy: 'none', updatedAt: new Date().toISOString() },
        fallback: { attempts: 0 },
        requestCount: 0,
      }),
    },
    tools: {},
    skills: [],
    state: {
      taskStatus: 'idle',
      prompt: '',
      mode: 'plan',
      activeModel: 'test-model',
      workspace: { root: '', indexedAt: null, files: [], contextLocked: false },
      metrics: {
        compression: { inputTokens: 0, compressedTokens: 0, ratio: 1, strategy: 'none', updatedAt: new Date().toISOString() },
        fallback: { attempts: 0 },
        requestCount: 0,
      },
      messages: [],
      preview: null,
      taskQueue: [],
    },
    subscribe: () => () => {},
    selectModel: async () => {},
    setApprovalHandler: () => {},
    run: async () => ({ content: '', model: 'test-model' }),
    cancel: () => {},
    clearHistory: async () => {},
    stop: () => {},
  } as unknown as MastraEngine;
}

test('status line tells the user to use Ctrl+J when the kitty query is unanswered', async () => {
  const stdin = new FakeStdin();
  const stdout = new FakeStdout();
  const instance = render(React.createElement(TerminalInterface, { engine: makeEngine() }), { stdin, stdout, stderr: new FakeStdout() });
  await sleep(450); // kitty detection timeout is 300ms; no answer arrives
  const text = stripAnsi(stdout.output);
  assert.match(text, /can't distinguish Shift\+Enter from Enter/);
  assert.match(text, /use Ctrl\+J for a new line/);
  assert.doesNotMatch(text, /kitty protocol active/);
  // Legacy mode-key fallback is advertised on non-kitty terminals.
  assert.match(text, /Ctrl\+E mode/);
  instance.unmount();
});

test('status line confirms kitty protocol when the terminal answers the query', async () => {
  const stdin = new FakeStdin();
  const stdout = new FakeStdout();
  const instance = render(React.createElement(TerminalInterface, { engine: makeEngine() }), { stdin, stdout, stderr: new FakeStdout() });
  await sleep(30);
  stdin.write('\x1b[?1u'); // kitty query answer: ESC[? flags u
  await sleep(50);
  const text = stripAnsi(stdout.output);
  assert.match(text, /kitty protocol active/);
  assert.match(text, /Shift\+Enter makes a new line/);
  assert.doesNotMatch(text, /can't distinguish/);
  // Ctrl+M stays the mode key on kitty terminals.
  assert.match(text, /Ctrl\+M mode/);
  instance.unmount();
});

test('Ctrl+J inserts a newline and the input box renders two rows', async () => {
  const stdin = new FakeStdin();
  const stdout = new FakeStdout();
  const instance = render(React.createElement(TerminalInterface, { engine: makeEngine() }), { stdin, stdout, stderr: new FakeStdout() });
  await sleep(30);
  stdin.write('ab');
  stdin.write('\x0a'); // Ctrl+J = LF: must insert a newline, not submit
  stdin.write('cd');
  await sleep(50);
  const text = stripAnsi(stdout.output);
  assert.match(text, /ab[\s\S]*cd▍/); // 'ab' and 'cd' rendered on separate rows, caret at line end
  assert.doesNotMatch(text, /abcd/); // not concatenated onto one row
  instance.unmount();
});

test('todos events render the visible plan panel with markers and progress', async () => {
  const stdin = new FakeStdin();
  const stdout = new FakeStdout();
  let listener: ((event: Parameters<MastraEngine['subscribe']>[0]) => void) | undefined;
  const engine = makeEngine();
  engine.subscribe = (handler) => { listener = handler; return () => {}; };
  const instance = render(React.createElement(TerminalInterface, { engine }), { stdin, stdout, stderr: new FakeStdout() });
  await sleep(30);
  listener!({ type: 'todos', todos: [
    { id: 't1', title: 'read the file', status: 'done' },
    { id: 't2', title: 'fix the bug', status: 'active' },
    { id: 't3', title: 'run tests', status: 'pending' },
  ] });
  await sleep(50);
  const text = stripAnsi(stdout.output);
  assert.match(text, /plan/);
  assert.match(text, /1\/3 done/);
  assert.match(text, /✓ read the file/);
  assert.match(text, /▸ fix the bug/);
  assert.match(text, /○ run tests/);
  instance.unmount();
});

test('current tool shows in the busy line during a run', async () => {
  const stdin = new FakeStdin();
  const stdout = new FakeStdout();
  let listener: ((event: Parameters<MastraEngine['subscribe']>[0]) => void) | undefined;
  const engine = makeEngine();
  engine.subscribe = (handler) => { listener = handler; return () => {}; };
  // Keep the run in flight so the busy line stays visible.
  engine.run = () => new Promise(() => {});
  const instance = render(React.createElement(TerminalInterface, { engine }), { stdin, stdout, stderr: new FakeStdout() });
  await sleep(30);
  stdin.write('do it');
  await sleep(30); // separate chunk so Ink parses '\r' as a return key, not paste
  stdin.write('\r'); // submit
  await sleep(50);
  listener!({ type: 'tool_start', tool: 'read_file', input: undefined });
  // Poll for the rendered frame (Ink flushes async) instead of a fixed sleep.
  let text = '';
  for (let i = 0; i < 30; i += 1) {
    text = stripAnsi(stdout.output);
    if (/now read_file/.test(text)) break;
    await sleep(20);
  }
  assert.match(text, /now read_file/);
  instance.unmount();
});

test('up arrow on an empty prompt recalls and the recalled prompt is re-submitted', async () => {
  const stdin = new FakeStdin();
  const stdout = new FakeStdout();
  const submitted: string[] = [];
  const engine = makeEngine();
  engine.run = async (prompt: string) => { submitted.push(prompt); return { content: 'ok', model: 'test-model' }; };
  // Isolate the persisted prompt history from the developer's real config.
  const configDir = path.join(os.tmpdir(), `oh-tui-hist-${Date.now()}`);
  const previous = process.env.OMNIHARNESS_CONFIG_DIR;
  process.env.OMNIHARNESS_CONFIG_DIR = configDir;
  const instance = render(React.createElement(TerminalInterface, { engine }), { stdin, stdout, stderr: new FakeStdout() });
  try {
    await sleep(120);
    stdin.write('alpha');
    await sleep(60); // separate chunk so Ink parses '\r' as a return key, not paste
    stdin.write('\r'); // submit => records 'alpha' in history
    await sleep(180);
    stdin.write('\x1b[A'); // up arrow: recalls 'alpha'
    await sleep(150); // let React re-render the recalled value into the input
    stdin.write('\r'); // resubmit the recalled prompt
    await sleep(150);
    assert.deepEqual(submitted, ['alpha', 'alpha']);
  } finally {
    instance.unmount();
    await fs.rm(configDir, { recursive: true, force: true });
    if (previous === undefined) delete process.env.OMNIHARNESS_CONFIG_DIR;
    else process.env.OMNIHARNESS_CONFIG_DIR = previous;
  }
});
