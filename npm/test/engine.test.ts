import assert from 'node:assert/strict';
import http from 'node:http';
import os from 'node:os';
import { test } from 'node:test';
import { createMastraEngine } from '../src/agent/mastraEngine.js';

test('run() introduces OmniHarness in a system message and reports the answering model', async () => {
  let body: { model: string; messages: Array<{ role: string; content: string }> } | undefined;
  const instance = http.createServer(async (req, res) => {
    const chunks: Buffer[] = [];
    for await (const chunk of req) chunks.push(Buffer.from(chunk));
    body = JSON.parse(Buffer.concat(chunks).toString()) as typeof body;
    res.writeHead(200, { 'content-type': 'application/json' });
    res.end(JSON.stringify({ model: 'aion/aion-labs/aion-3.0', choices: [{ message: { content: 'hey' } }] }));
  });
  instance.listen(0);
  const address = instance.address();
  if (!address || typeof address === 'string') throw new Error('server did not bind');
  try {
    const engine = createMastraEngine({ workspaceRoot: os.tmpdir(), endpoint: `http://localhost:${address.port}` });
    const result = await engine.run('hi');
    assert.equal(result.content, 'hey');
    assert.equal(result.model, 'aion/aion-labs/aion-3.0');
    assert.equal(body?.model, 'auto/best-coding');
    assert.equal(body?.messages[0]?.role, 'system');
    assert.match(body?.messages[0]?.content ?? '', /OmniHarness/);
    assert.match(body?.messages[0]?.content ?? '', /OmniRoute/);
    assert.equal(body?.messages.at(-1)?.content, 'hi');
  } finally { instance.close(); }
});
