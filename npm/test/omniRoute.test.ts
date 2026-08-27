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

test('normalizes endpoint and sends OmniRoute headers and auth', async () => {
  let received: Request | undefined;
  const live = server((request) => { received = request; return Response.json({ choices: [{ message: { content: 'ok' } }] }); });
  try {
    const client = new OmniRouteClient({ endpoint: `${live.url}/`, apiKey: 'secret' });
    const result = await client.chat('custom/combo', [{ role: 'user', content: 'hello' }]);
    assert.equal(client.endpoint, live.url);
    assert.equal(result.content, 'ok');
    assert.equal(received?.headers.get('authorization'), 'Bearer secret');
    assert.equal(received?.headers.get('x-omniharness-metrics'), 'tokens,compression,fallback,quota');
  } finally { live.close(); }
});

test('decodes empty and wrapped combo responses, and classifies HTTP errors', async () => {
  for (const payload of [[], { combos: [{ name: 'coding', models: [] }] }]) {
    const live = server(() => Response.json(payload));
    try { assert.deepEqual(await new OmniRouteClient({ endpoint: live.url }).listCombos(), payload.length === 0 ? [] : [{ name: 'coding', models: [] }]); }
    finally { live.close(); }
  }
  const live = server(() => new Response('denied', { status: 401 }));
  try { await assert.rejects(() => new OmniRouteClient({ endpoint: live.url }).listCombos(), (error: unknown) => error instanceof OmniRouteError && error.status === 401); }
  finally { live.close(); }
});
