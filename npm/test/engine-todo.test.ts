import assert from 'node:assert/strict';
import http from 'node:http';
import os from 'node:os';
import path from 'node:path';
import { promises as fs } from 'node:fs';
import { test } from 'node:test';
import { createMastraEngine, type HarnessEvent } from '../src/agent/mastraEngine.js';

function toSSE(payload: Record<string, unknown>): string {
  const message = (payload.message as Record<string, unknown> | undefined) ?? {};
  const toolCalls = Array.isArray(message.tool_calls) ? message.tool_calls : undefined;
  const chunks: string[] = [];
  if (toolCalls) chunks.push(`data: ${JSON.stringify({ choices: [{ index: 0, delta: { tool_calls: toolCalls }, finish_reason: null }] })}`);
  const content = typeof message.content === 'string' && message.content !== '' ? message.content : undefined;
  if (content) chunks.push(`data: ${JSON.stringify({ choices: [{ index: 0, delta: { content }, finish_reason: null }] })}`);
  chunks.push(`data: ${JSON.stringify({ choices: [{ index: 0, delta: {}, finish_reason: payload.finish_reason ?? 'stop' }] })}`);
  chunks.push('data: [DONE]');
  return chunks.join('\n');
}

function server(handler: (body: { model: string; messages: Array<{ role: string; content: string }>; tools: unknown }) => unknown) {
  const calls: Array<{ model: string; messages: Array<{ role: string; content: string }>; tools: unknown }> = [];
  const instance = http.createServer(async (req, res) => {
    const chunks: Buffer[] = [];
    for await (const chunk of req) chunks.push(Buffer.from(chunk));
    const body = JSON.parse(Buffer.concat(chunks).toString()) as { model: string; messages: Array<{ role: string; content: string }>; tools: unknown };
    calls.push(body);
    const payload = handler(body);
    res.writeHead(200, { 'content-type': 'text/event-stream' });
    res.end(toSSE(payload as Record<string, unknown>));
  });
  instance.listen(0);
  const address = instance.address();
  if (!address || typeof address === 'string') throw new Error('server did not bind');
  return { calls, url: `http://localhost:${address.port}`, close: () => instance.close() };
}

test('update_todo maintains the task queue, emits todos events, and threads results back', async () => {
  const workspace = await fs.mkdtemp(path.join(os.tmpdir(), 'oh-todo-'));
  const emitted: HarnessEvent[] = [];
  let index = 0;
  const todoCall = (action: string, extra: Record<string, string> = {}) => ({ index: index++, id: `call_${Math.random().toString(36).slice(2)}`, type: 'function', function: { name: 'update_todo', arguments: JSON.stringify({ action, ...extra }) } });
  const responses = [
    { finish_reason: 'tool_calls', message: { role: 'assistant', content: '', tool_calls: [todoCall('add', { title: 'read files' }), todoCall('add', { title: 'fix bug' }), todoCall('start')] } },
    { finish_reason: 'tool_calls', message: { role: 'assistant', content: '', tool_calls: [todoCall('complete')] } },
    { finish_reason: 'stop', message: { role: 'assistant', content: 'done' } },
  ];
  const live = server(() => responses.shift());
  try {
    const engine = await createMastraEngine({ workspaceRoot: workspace, endpoint: live.url, mode: 'build' });
    engine.subscribe((event) => emitted.push(event));
    const result = await engine.run('fix the bug');
    assert.equal(result.content, 'done');
    assert.deepEqual(engine.state.taskQueue.map((item) => [item.title, item.status]),
      [['read files', 'done'], ['fix bug', 'pending']]);
    const todoEvents = emitted.filter((event) => event.type === 'todos');
    assert.ok(todoEvents.length >= 3, `expected todos events, got ${todoEvents.length}`);
    // Tool results were fed back to the model so it sees the queue state.
    const last = live.calls.at(-1)!;
    assert.ok(last.messages.some((message) => message.role === 'tool' && /todo added: read files/.test(message.content)));
  } finally {
    live.close();
    await fs.rm(workspace, { recursive: true, force: true });
  }
});

test('crazy mode: risky tools auto-approve, memory persists, and the prompt says autonomous', async () => {
  const workspace = await fs.mkdtemp(path.join(os.tmpdir(), 'oh-crazy-'));
  const emitted: HarnessEvent[] = [];
  const call = { id: 'call_c', type: 'function', function: { name: 'write_file', arguments: JSON.stringify({ path: 'a.txt', content: 'x' }) } };
  const responses = [
    { finish_reason: 'tool_calls', message: { role: 'assistant', content: '', tool_calls: [call] } },
    { finish_reason: 'stop', message: { role: 'assistant', content: 'shipped' } },
  ];
  const live = server(() => responses.shift());
  try {
    const engine = await createMastraEngine({ workspaceRoot: workspace, endpoint: live.url, mode: 'crazy' });
    // A denial handler would block write_file in other modes; crazy mode must skip it.
    engine.setApprovalHandler(async () => ({ approved: false }));
    engine.subscribe((event) => emitted.push(event));
    const result = await engine.run('write a file');
    assert.equal(result.content, 'shipped');
    assert.ok(await fs.stat(path.join(workspace, 'a.txt')), 'write_file executed without approval');
    assert.ok(!emitted.some((event) => event.type === 'approval_requested'), 'no approval gate in crazy mode');
    const system = live.calls[0]!.messages[0]!;
    assert.match(system.content, /CRAZY MODE/);
    // Session outcome was appended to persistent memory.
    const memory = await fs.readFile(path.join(workspace, '.omniharness', 'memory.md'), 'utf8');
    assert.match(memory, /ran "write a file"/);
  } finally {
    live.close();
    await fs.rm(workspace, { recursive: true, force: true });
  }
});

test('run() reports the answering model and per-response compression on the text event', async () => {
  const workspace = await fs.mkdtemp(path.join(os.tmpdir(), 'oh-comp-'));
  const emitted: HarnessEvent[] = [];
  const instance = http.createServer(async (req, res) => {
    const chunks: Buffer[] = [];
    for await (const chunk of req) chunks.push(Buffer.from(chunk));
    res.writeHead(200, {
      'content-type': 'text/event-stream',
      'x-omniroute-input-tokens': '1200',
      'x-omniroute-compressed-tokens': '300',
      'x-omniroute-compression': 'caveman',
    });
    res.end(toSSE({ finish_reason: 'stop', message: { role: 'assistant', content: 'all good' } }));
  });
  instance.listen(0);
  const address = instance.address();
  if (!address || typeof address === 'string') throw new Error('no bind');
  try {
    const engine = await createMastraEngine({ workspaceRoot: workspace, endpoint: `http://localhost:${(address as { port: number }).port}` });
    engine.subscribe((event) => emitted.push(event));
    const result = await engine.run('hi');
    assert.equal(result.content, 'all good');
    const text = emitted.find((event) => event.type === 'text');
    assert.ok(text && text.type === 'text');
    assert.equal(text.model, 'auto/best-coding'); // no model in SSE → engine default
    assert.equal(text.compression?.savedTokens, 900);
    assert.ok(Math.abs((text.compression?.ratio ?? 1) - 0.25) < 1e-9);
  } finally {
    instance.close();
    await fs.rm(workspace, { recursive: true, force: true });
  }
});

test('semantic index survives a restart: unchanged files are not re-embedded', async () => {
  const workspace = await fs.mkdtemp(path.join(os.tmpdir(), 'oh-idxint-'));
  await fs.writeFile(path.join(workspace, 'notes.txt'), 'the auth module uses tokens for sessions', 'utf8');
  // Counts /v1/embeddings calls. First run embeds the query plus the file chunk
  // (2 calls); after a restart the chunk loads from the persisted index, so only the
  // query is embedded (1 call).
  let embedCalls = 0;
  const searchCall = () => ({ id: `call_s${Math.random().toString(36).slice(2)}`, index: 0, type: 'function', function: { name: 'semantic_search', arguments: JSON.stringify({ query: 'tokens' }) } });
  const makeChatResponses = () => [
    { finish_reason: 'tool_calls', message: { role: 'assistant', content: '', tool_calls: [searchCall()] } },
    { finish_reason: 'stop', message: { role: 'assistant', content: 'search done' } },
  ];
  // One server serves both the SSE chat stream and /v1/embeddings on the same port.
  const chatResponses = [...makeChatResponses(), ...makeChatResponses()];
  const instance = http.createServer(async (req, res) => {
    const chunks: Buffer[] = [];
    for await (const chunk of req) chunks.push(Buffer.from(chunk));
    const body = JSON.parse(Buffer.concat(chunks).toString()) as { model?: string; input?: unknown };      if (body.input !== undefined) {
        embedCalls += 1;
        const inputs = body.input as string[];
        const embedVec = Array.from({ length: 4 }, (_, i) => 0.1 * (i + 1));
        res.writeHead(200, { 'content-type': 'application/json' });
        res.end(JSON.stringify({ data: inputs.map(() => ({ embedding: embedVec })) }));
        return;
      }
    res.writeHead(200, { 'content-type': 'text/event-stream' });
    res.end(toSSE(chatResponses.shift() ?? { finish_reason: 'stop', message: { role: 'assistant', content: 'search done' } } as Record<string, unknown>));
  });
  instance.listen(0);
  const address = instance.address();
  if (!address || typeof address === 'string') throw new Error('no bind');
  const url = `http://localhost:${(address as { port: number }).port}`;
  try {
    // First engine embeds everything and persists.
    const first = await createMastraEngine({ workspaceRoot: workspace, endpoint: url });
    await first.run('search something');
    const fileExists = await fs.stat(path.join(workspace, '.omniharness', 'semantic-index.json')).then(() => true).catch(() => false);
    assert.ok(fileExists, 'index persisted to disk');
    // Second engine over the same workspace reloads the index and does not re-embed files.
    const second = await createMastraEngine({ workspaceRoot: workspace, endpoint: url });
    await second.run('search something again');
  } finally {
    instance.close();
    await fs.rm(workspace, { recursive: true, force: true });
  }
  // 2 calls on the first run (query + file chunk), 1 on the restart (query only).
  assert.equal(embedCalls, 3, `expected 3 embed calls across both runs, got ${embedCalls}`);
});

test('crazy mode injects persistent memory from previous sessions into the system prompt', async () => {
  const workspace = await fs.mkdtemp(path.join(os.tmpdir(), 'oh-mem-'));
  await fs.mkdir(path.join(workspace, '.omniharness'), { recursive: true });
  await fs.writeFile(path.join(workspace, '.omniharness', 'memory.md'), '- user hates the color red', 'utf8');
  const live = server(() => ({ finish_reason: 'stop', message: { role: 'assistant', content: 'ok' } }));
  try {
    const engine = await createMastraEngine({ workspaceRoot: workspace, endpoint: live.url, mode: 'crazy' });
    await engine.run('hi');
    const system = live.calls[0]!.messages[0]!;
    assert.match(system.content, /PERSISTENT MEMORY/);
    assert.match(system.content, /user hates the color red/);
  } finally {
    live.close();
    await fs.rm(workspace, { recursive: true, force: true });
  }
});
