import assert from 'node:assert/strict';
import http from 'node:http';
import os from 'node:os';
import path from 'node:path';
import { promises as fs } from 'node:fs';
import { test } from 'node:test';
import { createMastraEngine } from '../src/agent/mastraEngine.js';

function toSSE(payload: { message?: { role?: string; content?: string; tool_calls?: unknown }; finish_reason?: string }): string {
  const toolCalls = Array.isArray(payload.message?.tool_calls) ? payload.message.tool_calls : undefined;
  const chunks: string[] = [];
  const content = typeof payload.message?.content === 'string' && payload.message.content !== '' ? payload.message.content : undefined;
  if (toolCalls) chunks.push(`data: ${JSON.stringify({ choices: [{ index: 0, delta: { tool_calls: toolCalls }, finish_reason: null }] })}`);
  if (content) chunks.push(`data: ${JSON.stringify({ choices: [{ index: 0, delta: { content }, finish_reason: null }] })}`);
  chunks.push(`data: ${JSON.stringify({ choices: [{ index: 0, delta: {}, finish_reason: payload.finish_reason ?? 'stop' }] })}`);
  chunks.push('data: [DONE]');
  return chunks.join('\n');
}

function chatServer() {
  const calls: Array<{ model: string; messages: Array<{ role: string; content: string }> }> = [];
  const instance = http.createServer(async (req, res) => {
    const chunks: Buffer[] = [];
    for await (const chunk of req) chunks.push(Buffer.from(chunk));
    const body = JSON.parse(Buffer.concat(chunks).toString()) as { model: string; messages: Array<{ role: string; content: string }> };
    calls.push(body);
    res.writeHead(200, { 'content-type': 'text/event-stream' });
    res.end(toSSE({ finish_reason: 'stop', message: { role: 'assistant', content: 'ok' } }));
  });
  instance.listen(0);
  const address = instance.address();
  if (!address || typeof address === 'string') throw new Error('server did not bind');
  return { calls, url: `http://localhost:${address.port}`, close: () => instance.close() };
}

test('a new engine resumes prior transcript sent to the wire and persisted', async () => {
  const workspace = await fs.mkdtemp(path.join(os.tmpdir(), 'oh-resume-'));
  const live = chatServer();
  try {
    const first = await createMastraEngine({ workspaceRoot: workspace, endpoint: live.url });
    await first.run('remember the limes');
    const sessionFile = path.join(workspace, '.omniharness', 'session.json');
    const onDisk = await fs.readFile(sessionFile, 'utf8');
    assert.match(onDisk, /remember the limes/);
    // A second engine over the same workspace hydrates the transcript.
    const second = await createMastraEngine({ workspaceRoot: workspace, endpoint: live.url });
    assert.equal(second.state.messages.length, 2); // user + assistant
    assert.match(second.state.messages[0]!.content, /remember the limes/);
    await second.run('and now this');
    const last = live.calls.at(-1)!;
    assert.ok(last.messages.some((m) => m.content === 'remember the limes'), 'resumed history was sent to the model');
  } finally {
    live.close();
    await fs.rm(workspace, { recursive: true, force: true });
  }
});

test('cancel() aborts an in-flight run and reports a cancelled status', async () => {
  const workspace = await fs.mkdtemp(path.join(os.tmpdir(), 'oh-cancel-'));
  // Server accepts the request but never responds, so the run stays in flight.
  const instance = http.createServer(() => { /* never respond */ });
  instance.listen(0);
  const address = instance.address();
  if (!address || typeof address === 'string') throw new Error('no bind');
  try {
    const engine = await createMastraEngine({ workspaceRoot: workspace, endpoint: `http://localhost:${address.port}` });
    const runPromise = engine.run('long task');
    await new Promise((resolve) => setTimeout(resolve, 50));
    engine.cancel();
    const result = await runPromise;
    assert.equal(result.content, '(cancelled)');
    assert.equal(engine.state.taskStatus, 'cancelled');
  } finally {
    instance.closeAllConnections?.();
    instance.close();
    await fs.rm(workspace, { recursive: true, force: true });
  }
});

test('clearHistory() empties the transcript and resets the persisted session', async () => {
  const workspace = await fs.mkdtemp(path.join(os.tmpdir(), 'oh-clear-'));
  const live = chatServer();
  try {
    const engine = await createMastraEngine({ workspaceRoot: workspace, endpoint: live.url });
    await engine.run('build the thing');
    assert.equal(engine.state.messages.length, 2);
    await engine.clearHistory();
    assert.equal(engine.state.messages.length, 0);
    const second = await createMastraEngine({ workspaceRoot: workspace, endpoint: live.url });
    assert.equal(second.state.messages.length, 0);
  } finally {
    live.close();
    await fs.rm(workspace, { recursive: true, force: true });
  }
});