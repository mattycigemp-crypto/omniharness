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
    if (reasoning) chunks.push(`data: ${JSON.stringify({ choices: [{ index: 0, delta: { reasoning }, finish_reason: null }] })}`);
    if (content) chunks.push(`data: ${JSON.stringify({ choices: [{ index: 0, delta: { content }, finish_reason: null }] })}`);
    if (toolCalls && toolCalls.length > 0) chunks.push(`data: ${JSON.stringify({ choices: [{ index: 0, delta: { tool_calls: toolCalls }, finish_reason: null }] })}`);
    chunks.push(`data: ${JSON.stringify({ choices: [{ index: 0, delta: {}, finish_reason: choice.finish_reason ?? 'stop' }] })}`);
  }
  chunks.push('data: [DONE]');
  return chunks.join('\n');
}

function chatServer(handler: () => unknown) {
  const calls: Array<{ messages: Array<Record<string, unknown>>; tools?: Array<{ function: { name: string } }> }> = [];
  const instance = http.createServer(async (req, res) => {
    const chunks: Buffer[] = [];
    for await (const chunk of req) chunks.push(Buffer.from(chunk));
    const body = JSON.parse(Buffer.concat(chunks).toString()) as { messages: Array<Record<string, unknown>>; tools?: Array<{ function: { name: string } }> };
    calls.push(body);
    res.writeHead(200, { 'content-type': 'text/event-stream' });
    res.end(toSSE(handler() as ResponseEnvelope));
  });
  instance.listen(0);
  const address = instance.address();
  if (!address || typeof address === 'string') throw new Error('server did not bind');
  return { calls, url: `http://localhost:${address.port}`, close: () => instance.close() };
}

const stop = { choices: [{ finish_reason: 'stop', message: { content: 'ok' } }] };

test('streams reasoning and text deltas as they arrive', async () => {
  const emitted: HarnessEvent[] = [];
  const live = chatServer(() => ({
    choices: [{ finish_reason: 'stop', message: { content: 'hello', reasoning: 'thinking first' } }],
  }));
  try {
    const engine = await createMastraEngine({ workspaceRoot: os.tmpdir(), endpoint: live.url });
    engine.subscribe((event) => emitted.push(event));
    const result = await engine.run('hi');
    assert.equal(result.content, 'hello');
    assert.deepEqual(emitted.filter((e) => e.type === 'thinking_delta').map((e) => e.delta), ['thinking first']);
    assert.deepEqual(emitted.filter((e) => e.type === 'text_delta').map((e) => e.delta), ['hello']);
    assert.ok(emitted.some((e) => e.type === 'thinking' && e.text === 'thinking first'));
  } finally { live.close(); }
});

test('approval gate denies a risky write when the handler rejects it', async () => {
  const emitted: HarnessEvent[] = [];
  const responses = [
    { choices: [{ finish_reason: 'tool_calls', message: { content: '', tool_calls: [{ id: 'call_w', type: 'function', function: { name: 'write_file', arguments: JSON.stringify({ path: 'x.txt', content: 'secret' }) } }] } }] },
    { choices: [{ finish_reason: 'stop', message: { content: 'refused' } }] },
  ];
  const live = chatServer(() => responses.shift() ?? stop);
  try {
    const engine = await createMastraEngine({ workspaceRoot: os.tmpdir(), endpoint: live.url });
    engine.subscribe((event) => emitted.push(event));
    engine.setApprovalHandler(async (action) => ({ approved: action.tool !== 'write_file' }));
    const result = await engine.run('write a file');
    assert.equal(result.content, 'refused');
    assert.ok(emitted.some((e) => e.type === 'approval_requested' && e.tool === 'write_file'));
    const toolMsg = live.calls[1].messages.find((m) => m.role === 'tool');
    assert.match(String(toolMsg?.content ?? ''), /denied/);
  } finally { live.close(); }
});

test('approval gate approves a risky write and the file is created', async () => {
  const workspace = await fs.mkdtemp(path.join(os.tmpdir(), 'oh-approve-'));
  const responses = [
    { choices: [{ finish_reason: 'tool_calls', message: { content: '', tool_calls: [{ id: 'call_w2', type: 'function', function: { name: 'write_file', arguments: JSON.stringify({ path: 'made.txt', content: 'data' }) } }] } }] },
    { choices: [{ finish_reason: 'stop', message: { content: 'done' } }] },
  ];
  const live = chatServer(() => responses.shift() ?? stop);
  try {
    const engine = await createMastraEngine({ workspaceRoot: workspace, endpoint: live.url });
    engine.setApprovalHandler(async () => ({ approved: true }));
    await engine.run('create made.txt');
    assert.equal(await fs.readFile(path.join(workspace, 'made.txt'), 'utf8'), 'data');
  } finally { live.close(); await fs.rm(workspace, { recursive: true, force: true }); }
});

test('a chosen trust scope auto-approves later matching calls without re-prompting', async () => {
  const workspace = await fs.mkdtemp(path.join(os.tmpdir(), 'oh-trust-'));
  const responses = [
    { choices: [{ finish_reason: 'tool_calls', message: { content: '', tool_calls: [{ id: 'c1', type: 'function', function: { name: 'write_file', arguments: JSON.stringify({ path: 'one.txt', content: '1' }) } }] } }] },
    { choices: [{ finish_reason: 'tool_calls', message: { content: '', tool_calls: [{ id: 'c2', type: 'function', function: { name: 'write_file', arguments: JSON.stringify({ path: 'two.txt', content: '2' }) } }] } }] },
    { choices: [{ finish_reason: 'stop', message: { content: 'done' } }] },
  ];
  const live = chatServer(() => responses.shift() ?? stop);
  try {
    const engine = await createMastraEngine({ workspaceRoot: workspace, endpoint: live.url });
    let prompts = 0;
    engine.setApprovalHandler(async (action) => {
      prompts += 1;
      return { approved: true, trust: action.scopes[0]?.id };
    });
    await engine.run('write two files');
    assert.equal(prompts, 1, 'handler prompted once; the trust scope covered the second call');
    assert.equal(await fs.readFile(path.join(workspace, 'one.txt'), 'utf8'), '1');
    assert.equal(await fs.readFile(path.join(workspace, 'two.txt'), 'utf8'), '2');
  } finally { live.close(); await fs.rm(workspace, { recursive: true, force: true }); }
});

test('attachments ride the modality bridge as image parts and are listed', async () => {
  const workspace = await fs.mkdtemp(path.join(os.tmpdir(), 'oh-attach-'));
  const png = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==', 'base64');
  await fs.writeFile(path.join(workspace, 'pic.png'), png);
  const live = chatServer(() => stop);
  try {
    const engine = await createMastraEngine({ workspaceRoot: workspace, endpoint: live.url });
    const emitted: HarnessEvent[] = [];
    engine.subscribe((event) => emitted.push(event));
    const attached = await engine.attach(['pic.png']);
    assert.deepEqual(attached, [{ name: 'pic.png', kind: 'image', size: png.length }]);
    assert.ok(emitted.some((e) => e.type === 'attach' && e.name === 'pic.png' && e.kind === 'image'));
    await engine.run('describe it');
    const user = live.calls[0].messages[1] as { content: unknown };
    assert.ok(Array.isArray(user.content));
    const parts = user.content as Array<Record<string, unknown>>;
    assert.ok(parts.some((p) => p.type === 'text' && String(p.text).includes('pic.png')));
    const image = parts.find((p) => p.type === 'image_url') as { image_url?: { url: string } };
    assert.ok(image?.image_url?.url.startsWith('data:image/png;base64,'));
  } finally { live.close(); await fs.rm(workspace, { recursive: true, force: true }); }
});

test('non-image attachments are passed as a text note without data URLs', async () => {
  const workspace = await fs.mkdtemp(path.join(os.tmpdir(), 'oh-attach2-'));
  await fs.writeFile(path.join(workspace, 'notes.txt'), 'hello');
  const live = chatServer(() => stop);
  try {
    const engine = await createMastraEngine({ workspaceRoot: workspace, endpoint: live.url });
    await engine.attach(['notes.txt']);
    await engine.run('read it');
    const user = live.calls[0].messages[1] as { content: unknown };
    assert.equal(typeof user.content, 'string');
    assert.match(String(user.content), /notes\.txt/);
  } finally { live.close(); await fs.rm(workspace, { recursive: true, force: true }); }
});

test('attach fails loudly for missing files', async () => {
  const workspace = await fs.mkdtemp(path.join(os.tmpdir(), 'oh-attach3-'));
  const live = chatServer(() => stop);
  try {
    const engine = await createMastraEngine({ workspaceRoot: workspace, endpoint: live.url });
    await assert.rejects(engine.attach(['nope.txt']), /attach failed/);
  } finally { live.close(); await fs.rm(workspace, { recursive: true, force: true }); }
});

test('skills from OMNIHARNESS.md are loaded, offered to the model, and executable', async () => {
  const workspace = await fs.mkdtemp(path.join(os.tmpdir(), 'oh-skill-'));
  await fs.writeFile(path.join(workspace, 'OMNIHARNESS.md'), [
    '## greet',
    'description: Say hello to a name',
    'param: name string',
    'command: echo "hello {name}"',
  ].join('\n'));
  const responses = [
    { choices: [{ finish_reason: 'tool_calls', message: { content: '', tool_calls: [{ id: 'call_g', type: 'function', function: { name: 'greet', arguments: JSON.stringify({ name: 'world' }) } }] } }] },
    { choices: [{ finish_reason: 'stop', message: { content: 'greeted' } }] },
  ];
  const live = chatServer(() => responses.shift() ?? stop);
  try {
    const engine = await createMastraEngine({ workspaceRoot: workspace, endpoint: live.url, shellAllowed: true });
    assert.equal(engine.skills.length, 1);
    assert.equal(engine.skills[0]?.name, 'greet');
    await engine.run('use the greeting skill');
    assert.ok((live.calls[0].tools ?? []).map((tool) => tool.function.name).includes('greet'));
    const toolMsg = live.calls[1].messages.find((m) => m.role === 'tool');
    assert.match(String(toolMsg?.content ?? ''), /hello world/);
  } finally { live.close(); await fs.rm(workspace, { recursive: true, force: true }); }
});
test('CRAZY runSwarm fans pending todos across parallel workers and drains the queue', async () => {
  const workspace = await fs.mkdtemp(path.join(os.tmpdir(), 'oh-swarm-'));
  // Every worker turn just says "ok" (no tool calls) so each claimed todo completes in one round.
  const live = chatServer(() => stop);
  const events: HarnessEvent[] = [];
  try {
    const engine = await createMastraEngine({ workspaceRoot: workspace, endpoint: live.url, mode: 'crazy' });
    engine.subscribe((event) => events.push(event));
    // Seed a plan of 4 pending todos directly on the engine state.
    (engine.state as { taskQueue: unknown }).taskQueue = [
      { id: 'a', title: 'task one', status: 'pending' },
      { id: 'b', title: 'task two', status: 'pending' },
      { id: 'c', title: 'task three', status: 'pending' },
      { id: 'd', title: 'task four', status: 'pending' },
    ];
    await engine.runSwarm({ maxAgents: 3 });
    assert.equal(engine.state.taskQueue.filter((t) => t.status === 'done').length, 4, 'every todo completed');
    const agentIds = new Set(events.filter((e) => e.type === 'agent').map((e) => (e as { id: string }).id));
    assert.equal(agentIds.size, 3, 'three worker lanes spawned');
    assert.ok(events.some((e) => e.type === 'agent' && e.status === 'working'));
  } finally { live.close(); await fs.rm(workspace, { recursive: true, force: true }); }
});

test('runSwarm is inert outside CRAZY mode', async () => {
  const workspace = await fs.mkdtemp(path.join(os.tmpdir(), 'oh-swarm-off-'));
  const live = chatServer(() => stop);
  try {
    const engine = await createMastraEngine({ workspaceRoot: workspace, endpoint: live.url, mode: 'build' });
    (engine.state as { taskQueue: unknown }).taskQueue = [{ id: 'a', title: 'x', status: 'pending' }, { id: 'b', title: 'y', status: 'pending' }];
    await engine.runSwarm({ maxAgents: 3 });
    assert.equal(engine.state.taskQueue.every((t) => t.status === 'pending'), true, 'nothing ran');
  } finally { live.close(); await fs.rm(workspace, { recursive: true, force: true }); }
});
