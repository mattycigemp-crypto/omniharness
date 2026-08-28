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
