import assert from 'node:assert/strict';
import http from 'node:http';
import os from 'node:os';
import path from 'node:path';
import { promises as fs } from 'node:fs';
import { test } from 'node:test';
import { PassThrough, Writable } from 'node:stream';
import React from 'react';
import { render } from 'ink';
import { createMastraEngine } from '../src/agent/mastraEngine.js';
import { TerminalInterface } from '../src/ui/terminalInterface.js';
import type { MastraEngine } from '../src/agent/mastraEngine.js';
import type { AgentMode } from '../src/types/index.js';

// --- minimal SSE gateway -----------------------------------------------------

interface Envelope { choices?: Array<{ finish_reason?: string; message?: Record<string, unknown> }> }

function toSSE(payload: Envelope): string {
  const out: string[] = [];
  for (const choice of payload.choices ?? []) {
    const m = choice.message ?? {};
    if (typeof m.content === 'string') out.push(`data: ${JSON.stringify({ choices: [{ index: 0, delta: { content: m.content }, finish_reason: null }] })}`);
    if (Array.isArray(m.tool_calls) && m.tool_calls.length > 0) out.push(`data: ${JSON.stringify({ choices: [{ index: 0, delta: { tool_calls: m.tool_calls }, finish_reason: null }] })}`);
    out.push(`data: ${JSON.stringify({ choices: [{ index: 0, delta: {}, finish_reason: choice.finish_reason ?? 'stop' }] })}`);
  }
  out.push('data: [DONE]');
  return out.join('\n');
}

function chatServer(handler: (body: { messages: Array<{ role: string; content: unknown }> }) => Envelope) {
  const calls: Array<{ messages: Array<{ role: string; content: string }> }> = [];
  const server = http.createServer(async (req, res) => {
    const raw: Buffer[] = [];
    for await (const c of req) raw.push(Buffer.from(c));
    const body = JSON.parse(Buffer.concat(raw).toString());
    calls.push(body);
    res.writeHead(200, { 'content-type': 'text/event-stream' });
    res.end(toSSE(handler(body)));
  });
  server.listen(0);
  const addr = server.address();
  if (!addr || typeof addr === 'string') throw new Error('no bind');
  return { calls, url: `http://localhost:${addr.port}`, close: () => server.close() };
}

// --- 1. each mode shapes the system frame ----------------------------------

const MODE_MARKERS: Array<[AgentMode, RegExp, boolean]> = [
  ['plan', /You are in PLAN mode/, false],
  ['build', /You are in BUILD mode/, true], // build carries the WORK LOGIC discipline
  ['research', /You are in RESEARCH mode/, false],
  ['crazy', /You are in CRAZY MODE/, false],
];

for (const [mode, marker, hasWorkLogic] of MODE_MARKERS) {
  test(`${mode} mode: system frame carries its own instructions`, async () => {
    const live = chatServer(() => ({ choices: [{ finish_reason: 'stop', message: { content: 'ok' } }] }));
    try {
      const engine = await createMastraEngine({ workspaceRoot: os.tmpdir(), endpoint: live.url, mode });
      await engine.run('hi');
      const system = live.calls[0].messages[0];
      assert.equal(system.role, 'system');
      assert.match(system.content, marker);
      if (hasWorkLogic) assert.match(system.content, /WORK LOGIC/);
      else assert.doesNotMatch(system.content, /WORK LOGIC/);
    } finally { live.close(); }
  });
}

// --- 2. approval gating differs: crazy auto-approves, the rest prompt -------

function writeThenStop(pathName: string): Envelope[] {
  return [
    { choices: [{ finish_reason: 'tool_calls', message: { content: '', tool_calls: [{ index: 0, id: 'w1', type: 'function', function: { name: 'write_file', arguments: JSON.stringify({ path: pathName, content: 'data' }) } }] } }] },
    { choices: [{ finish_reason: 'stop', message: { content: 'done' } }] },
  ];
}

for (const mode of ['plan', 'build', 'research'] as AgentMode[]) {
  test(`${mode} mode: a high-risk tool goes through the approval gate`, async () => {
    const ws = await fs.mkdtemp(path.join(os.tmpdir(), `oh-mode-${mode}-`));
    const responses = writeThenStop('made.txt');
    const live = chatServer(() => responses.shift() ?? { choices: [{ finish_reason: 'stop', message: { content: 'ok' } }] });
    try {
      const engine = await createMastraEngine({ workspaceRoot: ws, endpoint: live.url, mode });
      let prompted = 0;
      engine.setApprovalHandler(async () => { prompted += 1; return { approved: false }; });
      await engine.run('write a file');
      assert.equal(prompted, 1, 'the approval handler was consulted');
      await assert.rejects(fs.readFile(path.join(ws, 'made.txt')), 'denied write never hit disk');
    } finally { live.close(); await fs.rm(ws, { recursive: true, force: true }); }
  });
}

test('crazy mode: high-risk tools are auto-approved (handler never consulted)', async () => {
  const ws = await fs.mkdtemp(path.join(os.tmpdir(), 'oh-mode-crazy-'));
  const responses = writeThenStop('auto.txt');
  const live = chatServer(() => responses.shift() ?? { choices: [{ finish_reason: 'stop', message: { content: 'ok' } }] });
  try {
    const engine = await createMastraEngine({ workspaceRoot: ws, endpoint: live.url, mode: 'crazy' });
    let prompted = 0;
    engine.setApprovalHandler(async () => { prompted += 1; return { approved: false }; });
    await engine.run('write a file');
    assert.equal(prompted, 0, 'crazy mode skips the approval gate');
    assert.equal(await fs.readFile(path.join(ws, 'auto.txt'), 'utf8'), 'data');
  } finally { live.close(); await fs.rm(ws, { recursive: true, force: true }); }
});

// --- 3. the swarm fan-out is crazy-only from the UI ------------------------

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
  _write(chunk: Buffer | string, _e: BufferEncoding, cb: (err?: Error | null) => void): void { this.output += chunk.toString(); cb(); }
}
const sleep = (ms: number): Promise<void> => new Promise((r) => setTimeout(r, ms));
const strip = (s: string): string => s.replace(/\x1b\[[0-9;?]*[A-Za-z]/g, '').replace(/\x1b[()][0-9A-Z]/g, '');

function fakeEngine(mode: AgentMode, onRunSwarm: () => void): MastraEngine {
  return {
    client: { listCombos: async () => [], endpoint: 'omniroute', snapshotMetrics: () => ({ compression: { inputTokens: 0, compressedTokens: 0, ratio: 1, strategy: 'none', updatedAt: '' }, fallback: { attempts: 0 }, requestCount: 0 }) },
    skills: [], tools: {},
    state: {
      taskStatus: 'idle', prompt: '', mode, activeModel: 'test-model',
      workspace: { root: '', indexedAt: null, files: [], contextLocked: false },
      metrics: { compression: { inputTokens: 0, compressedTokens: 0, ratio: 1, strategy: 'none', updatedAt: '' }, fallback: { attempts: 0 }, requestCount: 0 },
      messages: [], preview: null,
      taskQueue: [
        { id: 'a', title: 'one', status: 'pending' },
        { id: 'b', title: 'two', status: 'pending' },
      ],
    },
    subscribe: () => () => {},
    selectModel: async () => {},
    setApprovalHandler: () => {},
    run: async () => ({ content: 'planned', model: 'test-model' }),
    runSwarm: async () => { onRunSwarm(); },
    cancel: () => {},
    clearHistory: async () => {},
    stop: () => {},
  } as unknown as MastraEngine;
}

for (const mode of ['plan', 'build', 'research'] as AgentMode[]) {
  test(`${mode} mode: a multi-todo plan does NOT trigger the swarm`, async () => {
    let swarmed = 0;
    const engine = fakeEngine(mode, () => { swarmed += 1; });
    const stdin = new FakeStdin();
    const stdout = new FakeStdout();
    const instance = render(React.createElement(TerminalInterface, { engine }), { stdin, stdout, stderr: new FakeStdout() });
    await sleep(40);
    stdin.write('do the work');
    await sleep(40); stdin.write('\r');
    await sleep(150);
    assert.equal(swarmed, 0, `${mode} never fans out`);
    instance.unmount();
  });
}

test('crazy mode: a multi-todo plan triggers the swarm from the UI', async () => {
  let swarmed = 0;
  const engine = fakeEngine('crazy', () => { swarmed += 1; });
  const stdin = new FakeStdin();
  const stdout = new FakeStdout();
  const instance = render(React.createElement(TerminalInterface, { engine }), { stdin, stdout, stderr: new FakeStdout() });
  await sleep(40);
  stdin.write('do the work');
  await sleep(40); stdin.write('\r');
  await sleep(200);
  assert.equal(swarmed, 1, 'crazy fans out once the plan has 2+ pending todos');
  instance.unmount();
});

// --- 4. Ctrl+E cycles every mode in the UI --------------------------------

test('Ctrl+E cycles the visible mode plan → build → research → crazy → plan', async () => {
  const engine = fakeEngine('plan', () => {});
  (engine.state as { taskQueue: unknown[] }).taskQueue = [];
  const stdin = new FakeStdin();
  const stdout = new FakeStdout();
  const instance = render(React.createElement(TerminalInterface, { engine }), { stdin, stdout, stderr: new FakeStdout() });
  await sleep(40);
  const seen: string[] = [];
  for (const expected of ['build', 'research', 'crazy', 'plan']) {
    stdin.write('\x05'); // Ctrl+E
    await sleep(60);
    const text = strip(stdout.output);
    assert.match(text, new RegExp(`mode → ${expected}`), `cycled to ${expected}`);
    seen.push(expected);
  }
  assert.deepEqual(seen, ['build', 'research', 'crazy', 'plan']);
  instance.unmount();
});
