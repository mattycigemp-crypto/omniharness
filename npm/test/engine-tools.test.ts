import assert from 'node:assert/strict';
import http from 'node:http';
import os from 'node:os';
import path from 'node:path';
import { promises as fs } from 'node:fs';
import { test } from 'node:test';
import { createMastraEngine, type HarnessEvent } from '../src/agent/mastraEngine.js';

interface ResponseEnvelope { choices?: Array<{ finish_reason?: string; message?: Record<string, unknown> }> }

function toSSE(payload: ResponseEnvelope): string {
  const chunks: string[] = [];
  for (const choice of payload.choices ?? []) {
    const message = choice.message ?? {};
    const reasoning = typeof message.reasoning === 'string' ? message.reasoning : undefined;
    const content = typeof message.content === 'string' ? message.content : undefined;
    const toolCalls = Array.isArray(message.tool_calls) ? message.tool_calls : undefined;
    if (reasoning) chunks.push(`data: ${JSON.stringify({ model: 'test/model', choices: [{ index: 0, delta: { reasoning }, finish_reason: null }] })}`);
    if (content) chunks.push(`data: ${JSON.stringify({ model: 'test/model', choices: [{ index: 0, delta: { content }, finish_reason: null }] })}`);
    if (toolCalls && toolCalls.length > 0) chunks.push(`data: ${JSON.stringify({ model: 'test/model', choices: [{ index: 0, delta: { tool_calls: toolCalls }, finish_reason: null }] })}`);
    chunks.push(`data: ${JSON.stringify({ model: 'test/model', choices: [{ index: 0, delta: {}, finish_reason: choice.finish_reason ?? 'stop' }] })}`);
  }
  chunks.push('data: [DONE]');
  return chunks.join('\n');
}

function chatServer(handler: (body: { model: string; messages: unknown[] }) => unknown) {
  const calls: Array<{ model: string; messages: unknown[]; tools: unknown }> = [];
  const instance = http.createServer(async (req, res) => {
    const chunks: Buffer[] = [];
    for await (const chunk of req) chunks.push(Buffer.from(chunk));
    const body = JSON.parse(Buffer.concat(chunks).toString()) as { model: string; messages: unknown[]; tools: unknown };
    calls.push(body);
    const payload = handler(body) as ResponseEnvelope;
    res.writeHead(200, { 'content-type': 'text/event-stream' });
    res.end(toSSE(payload));
  });
  instance.listen(0);
  const address = instance.address();
  if (!address || typeof address === 'string') throw new Error('server did not bind');
  return { calls, url: `http://localhost:${address.port}`, close: () => instance.close() };
}

test('run() sends tools, executes a tool call, threads the result back, and emits events', async () => {
  const workspace = await fs.mkdtemp(path.join(os.tmpdir(), 'oh-engine-'));
  await fs.writeFile(path.join(workspace, 'greeting.txt'), 'hello world');
  const emitted: HarnessEvent[] = [];
  const responses = [
    {
      choices: [{ finish_reason: 'tool_calls', message: {
        role: 'assistant', content: '', reasoning: 'need to read the file',
        tool_calls: [{ id: 'call_1', type: 'function', function: { name: 'read_file', arguments: JSON.stringify({ path: 'greeting.txt' }) } }],
      } }],
    },
    { choices: [{ finish_reason: 'stop', message: { role: 'assistant', content: 'the file says hello world' } }] },
  ];
  const live = chatServer(() => responses.shift());
  try {
    const engine = await createMastraEngine({ workspaceRoot: workspace, endpoint: live.url, mode: 'build' });
    engine.subscribe((event) => emitted.push(event));
    const result = await engine.run('what is in greeting.txt?');
    assert.equal(result.content, 'the file says hello world');
    assert.equal(result.model, 'test/model');

    // First request carries tools and starts with the user message.
    const first = live.calls[0];
    assert.equal(first.model, 'auto/best-coding');
    assert.deepEqual((first.tools as Array<{ function: { name: string } }>).map((entry) => entry.function.name),
      ['read_file', 'write_file', 'run_command', 'index_workspace', 'git_diff', 'start_preview']);

    // Model's tool_calls message is fed back, then a tool result with the call id.
    const second = live.calls[1];
    const fedMessages = second.messages as Array<Record<string, unknown>>;
    const assistantMsg = fedMessages.find((m) => Array.isArray(m.tool_calls));
    assert.ok(assistantMsg, 'assistant tool_calls message was fed back');
    const toolMsg = fedMessages.find((m) => (m as { role: string }).role === 'tool');
    assert.equal((toolMsg as { tool_call_id?: string }).tool_call_id, 'call_1');
    assert.match(String((toolMsg as { content?: string }).content), /hello world/);

    // Events surfaced thinking + tool start + tool result.
    assert.ok(emitted.some((event) => event.type === 'thinking' && event.text.includes('need to read')));
    assert.ok(emitted.some((event) => event.type === 'tool_start' && event.tool === 'read_file'));
    assert.ok(emitted.some((event) => event.type === 'tool_result' && (event as { summary: string }).summary.includes('hello world')));
  } finally {
    live.close();
    await fs.rm(workspace, { recursive: true, force: true });
  }
});

test('mode shapes the system prompt', async () => {
  const live = chatServer(() => ({ choices: [{ finish_reason: 'stop', message: { role: 'assistant', content: 'ok' } }] }));
  try {
    const engine = await createMastraEngine({ workspaceRoot: os.tmpdir(), endpoint: live.url, mode: 'plan' });
    await engine.run('hi');
    const system = (live.calls[0].messages as Array<{ role: string; content: string }>)[0];
    assert.equal(system.role, 'system');
    assert.match(system.content, /You are in PLAN mode/);
  } finally { live.close(); }
});

test('unknown tool is surfaced as an error result, not a crash', async () => {
  const responses = [
    { choices: [{ finish_reason: 'tool_calls', message: { role: 'assistant', content: '', tool_calls: [{ id: 'call_x', type: 'function', function: { name: 'does_not_exist', arguments: '{}' } }] } }] },
    { choices: [{ finish_reason: 'stop', message: { role: 'assistant', content: 'done' } }] },
  ];
  const live = chatServer(() => responses.shift() ?? { choices: [{ finish_reason: 'stop', message: { role: 'assistant', content: 'done' } }] });
  try {
    const engine = await createMastraEngine({ workspaceRoot: os.tmpdir(), endpoint: live.url });
    const result = await engine.run('use a mystery tool');
    assert.equal(result.content, 'done');
  } finally { live.close(); }
});

test('tool loop is capped to avoid runaway turns', async () => {
  const live = chatServer(() => ({ choices: [{ finish_reason: 'tool_calls', message: { role: 'assistant', content: '', tool_calls: [{ id: 'call_loop', type: 'function', function: { name: 'read_file', arguments: JSON.stringify({ path: 'a' }) } }] } }] }));
  try {
    const engine = await createMastraEngine({ workspaceRoot: os.tmpdir(), endpoint: live.url });
    const result = await engine.run('loop forever');
    assert.match(result.content, /too many tool turns/);
  } finally { live.close(); }
});