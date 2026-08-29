import assert from 'node:assert/strict';
import http from 'node:http';
import { test } from 'node:test';
import { OmniRouteClient, OmniRouteError } from '../src/config/omniRoute.js';

function server(handler: (request: Request) => Response): { url: string; close: () => void } {
  const instance = http.createServer(async (req, res) => {
    const chunks: Buffer[] = [];
    for await (const chunk of req) chunks.push(Buffer.from(chunk));
    const response = handler(new Request(`http://localhost${req.url}`, { method: req.method, headers: req.headers as Record<string, string>, body: chunks.length ? Buffer.concat(chunks) : undefined }));
    res.writeHead(response.status, Object.fromEntries(response.headers));
    res.end(await response.text());
  });
  instance.listen(0);
  const address = instance.address();
  if (!address || typeof address === 'string') throw new Error('server did not bind');
  return { url: `http://localhost:${address.port}`, close: () => instance.close() };
}

test('normalizes endpoint, sends OmniRoute headers and auth, reports the model that answered', async () => {
  let received: Request | undefined;
  const live = server((request) => { received = request; return Response.json({ model: 'aion/aion-labs/aion-3.0', choices: [{ message: { content: 'ok' } }] }); });
  try {
    const client = new OmniRouteClient({ endpoint: `${live.url}/`, apiKey: 'secret' });
    const result = await client.chat('custom/combo', [{ role: 'user', content: 'hello' }]);
    assert.equal(client.endpoint, live.url);
    assert.equal(result.content, 'ok');
    assert.equal(result.model, 'aion/aion-labs/aion-3.0');
    assert.equal(received?.headers.get('authorization'), 'Bearer secret');
    assert.equal(received?.headers.get('x-omniharness-metrics'), 'tokens,compression,fallback,quota');
  } finally { live.close(); }
});

test('decodes empty and wrapped combo responses, and classifies HTTP errors', async () => {
  const envelopes: unknown[] = [
    [],
    { combos: [{ name: 'coding', models: [] }] },
    { object: 'list', data: [{ name: 'free-stack', strategy: 'auto', models: [{ kind: 'model', model: 'if/kimi-k2-thinking' }] }] },
  ];
  for (const payload of envelopes) {
    const live = server(() => Response.json(payload));
    try {
      const combos = await new OmniRouteClient({ endpoint: live.url }).listCombos();
      assert.equal(combos.length, Array.isArray(payload) ? 0 : 1);
      if (!Array.isArray(payload)) assert.equal(combos[0]?.name, 'combos' in (payload as object) ? 'coding' : 'free-stack');
    }
    finally { live.close(); }
  }
  const live = server(() => new Response('denied', { status: 401 }));
  try { await assert.rejects(() => new OmniRouteClient({ endpoint: live.url }).listCombos(), (error: unknown) => error instanceof OmniRouteError && error.status === 401); }
  finally { live.close(); }
});

test('chat() forwards tools and surfaces reasoning, finish_reason and tool_calls', async () => {
  let received: Request | undefined;
  const live = server((request) => {
    received = request;
    return Response.json({ model: 'test/model', choices: [{ finish_reason: 'tool_calls', message: {
      role: 'assistant', content: '', reasoning: 'I will read it',
      tool_calls: [{ id: 'call_9', type: 'function', function: { name: 'read_file', arguments: '{"path":"a"}' } }],
    } }] });
  });
  try {
    const client = new OmniRouteClient({ endpoint: live.url });
    const result = await client.chat('combo', [{ role: 'user', content: 'hi' }], {
      tools: [{ type: 'function', function: { name: 'read_file', description: 'read', parameters: { type: 'object', properties: {} } } }],
    });
    assert.equal(result.finishReason, 'tool_calls');
    assert.equal(result.reasoning, 'I will read it');
    assert.equal(result.toolCalls?.[0]?.function.name, 'read_file');
    assert.equal(result.toolCalls?.[0]?.id, 'call_9');
    const body = JSON.parse(await received!.text());
    assert.equal(body.tools[0].function.name, 'read_file');
    assert.equal(body.stream, false);
  } finally { live.close(); }
});

test('embed() posts to /v1/embeddings and returns vectors, rejecting invalid shapes', async () => {
  let received: Request | undefined;
  const live = server((request) => {
    received = request;
    return Response.json({ data: [{ embedding: [0.1, 0.2] }, { embedding: [0.3, 0.4] }] });
  });
  try {
    const client = new OmniRouteClient({ endpoint: live.url });
    const vectors = await client.embed(['alpha', 'beta']);
    assert.deepEqual(vectors, [[0.1, 0.2], [0.3, 0.4]]);
    const body = JSON.parse(await received!.text());
    assert.equal(body.model, 'gemini-embedding-001');
    assert.deepEqual(body.input, ['alpha', 'beta']);
  } finally { live.close(); }
  const bad = server(() => Response.json({ error: 'nope' }));
  try { await assert.rejects(() => new OmniRouteClient({ endpoint: bad.url }).embed(['x']), (error: unknown) => error instanceof OmniRouteError && error.status === 200); }
  finally { bad.close(); }
});

test('chat() surfaces per-response compression from x-omniroute headers', async () => {
  const live = server((request) => {
    const response = Response.json({ model: 'test/model', choices: [{ message: { role: 'assistant', content: 'ok' } }] });
    response.headers.set('x-omniroute-input-tokens', '1000');
    response.headers.set('x-omniroute-compressed-tokens', '200');
    response.headers.set('x-omniroute-compression', 'rtk');
    return response;
  });
  try {
    const client = new OmniRouteClient({ endpoint: live.url });
    const result = await client.chat('combo', [{ role: 'user', content: 'hi' }]);
    assert.equal(result.compression?.inputTokens, 1000);
    assert.equal(result.compression?.compressedTokens, 200);
    assert.equal(result.compression?.savedTokens, 800);
    assert.ok(Math.abs((result.compression?.ratio ?? 1) - 0.2) < 1e-9);
    assert.equal(result.compression?.strategy, 'rtk');
  } finally { live.close(); }
  // No savings when compression headers are absent or uncompressed.
  const bare = server(() => Response.json({ model: 'm', choices: [{ message: { role: 'assistant', content: 'ok' } }] }));
  try { assert.equal((await new OmniRouteClient({ endpoint: bare.url }).chat('m', [])).compression, undefined); }
  finally { bare.close(); }
});

// Fake MCP streamable-HTTP transport: initialize establishes a session, then
// tools/list and tools/call answer JSON-RPC calls carrying that session header.
function mcpServer(config: { tools: unknown[]; callResult: { content: { type: string; text: string }[]; isError?: boolean } }): { url: string; close: () => void } {
  const instance = http.createServer(async (req, res) => {
    const chunks: Buffer[] = [];
    for await (const chunk of req) chunks.push(Buffer.from(chunk));
    const rawBody = chunks.length ? Buffer.concat(chunks) : undefined;
    const request = new Request(`http://localhost${req.url}`, { method: req.method, headers: req.headers as Record<string, string>, body: rawBody });
    const raw = (rawBody && rawBody.length > 0 ? JSON.parse(rawBody.toString('utf8')) : {}) as { method?: string };
    let body: Response;
    if (raw.method === 'initialize') {
      body = Response.json({ jsonrpc: '2.0', id: 1, result: { protocolVersion: '2025-03-26', capabilities: {}, serverInfo: { name: 'omniroute', version: '3.8.50' } } });
      body.headers.set('mcp-session-id', 'sess-123');
    } else if (raw.method === 'notifications/initialized') {
      body = new Response(null, { status: 202 });
    } else if (raw.method === 'tools/list') {
      body = Response.json({ jsonrpc: '2.0', id: 2, result: { tools: config.tools } });
    } else {
      body = Response.json({ jsonrpc: '2.0', id: 2, result: config.callResult });
    }
    res.writeHead(body.status, Object.fromEntries(body.headers));
    res.end(await body.text());
  });
  instance.listen(0);
  const address = instance.address();
  if (!address || typeof address === 'string') throw new Error('server did not bind');
  return { url: `http://localhost:${address.port}`, close: () => instance.close() };
}

test('with a management token, listMcpTools() discovers the catalog and callMcpTool() runs it', async () => {
  const mcp = mcpServer({
    tools: [{ name: 'omniroute_check_quota', description: 'Quota used/total', inputSchema: { type: 'object', properties: {} } }],
    callResult: { content: [{ type: 'text', text: 'quota ok' }] },
  });
  try {
    const client = new OmniRouteClient({ endpoint: mcp.url, mgmtToken: 'sk-mgmt' });
    assert.equal(client.hasMcpToken, true);
    const tools = await client.listMcpTools();
    assert.equal(tools.length, 1);
    assert.equal(tools[0]?.name, 'omniroute_check_quota');
    assert.equal(tools[0]?.description, 'Quota used/total');
    assert.deepEqual(tools[0]?.inputSchema, { type: 'object', properties: {} });
    const result = await client.callMcpTool('omniroute_check_quota', {});
    assert.equal(result, 'quota ok');
  } finally { mcp.close(); }
});

test('without a management token, MCP discovery is inert', async () => {
  const live = server(() => Response.json({ ok: true }));
  try {
    const client = new OmniRouteClient({ endpoint: live.url });
    assert.equal(client.hasMcpToken, false);
    assert.deepEqual(await client.listMcpTools(), []);
  } finally { live.close(); }
});