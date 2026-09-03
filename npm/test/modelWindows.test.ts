import assert from 'node:assert/strict';
import { test } from 'node:test';
import { DEFAULT_WINDOW, contextMeter, meterBar, windowFor, windowIndex } from '../src/ui/modelWindows.js';

test('windowFor resolves by longest matching pattern across model and provider', () => {
  assert.equal(windowFor('claude-sonnet-4-20250101'), 200_000);
  assert.equal(windowFor('gpt-4o-mini'), 128_000);
  assert.equal(windowFor('auto/best-coding', 'gemini-2.5-pro'), 1_048_576);
  assert.equal(windowFor('something-unknown'), DEFAULT_WINDOW);
});

test('contextMeter zones follow the 70 / 90 thresholds', () => {
  assert.equal(contextMeter(50_000, 'claude').zone, 'ok');
  assert.equal(contextMeter(150_000, 'claude').zone, 'warn');
  assert.equal(contextMeter(185_000, 'claude').zone, 'danger');
  assert.equal(contextMeter(9_999_999, 'claude').fraction, 1);
  assert.equal(contextMeter(-5, 'claude').used, 0);
});

test('meterBar fills proportionally to the given cell count', () => {
  assert.equal(meterBar(0, 10), '░'.repeat(10));
  assert.equal(meterBar(1, 10), '█'.repeat(10));
  assert.equal(meterBar(0.5, 10), `${'█'.repeat(5)}${'░'.repeat(5)}`);
});

test('windowIndex keys the catalog by id and by the bare name after a provider prefix', () => {
  const index = windowIndex([
    { id: 'cc/claude-sonnet-4-6', contextLength: 200_000 },
    // A dual-mode mirror of the same model: the first claim on the bare name stands.
    { id: 'claude/claude-sonnet-4-6', contextLength: 1 },
    { id: 'auto/best-coding', contextLength: 1_048_576 },
    { id: 'openai/gpt-5.4' },
    { id: 'broken/model', contextLength: 0 },
    { id: '   ', contextLength: 5 },
  ]);
  assert.equal(index.get('cc/claude-sonnet-4-6'), 200_000);
  assert.equal(index.get('claude/claude-sonnet-4-6'), 1);
  assert.equal(index.get('claude-sonnet-4-6'), 200_000);
  assert.equal(index.get('auto/best-coding'), 1_048_576);
  assert.equal(index.get('best-coding'), 1_048_576);
  assert.equal(index.has('openai/gpt-5.4'), false);
  assert.equal(index.has('broken/model'), false);
  assert.equal(index.size, 5);
});

test('windowFor prefers the catalog — exact id, then bare name — and falls back to the table', () => {
  const known = windowIndex([
    { id: 'cc/claude-sonnet-4-6', contextLength: 1_000_000 },
    { id: 'zai/glm-5', contextLength: 204_800 },
  ]);
  // Exact id wins over the substring table's 200k for "claude".
  assert.equal(windowFor('cc/claude-sonnet-4-6', undefined, known), 1_000_000);
  // The model OmniRoute reports carries no prefix; the bare name finds it.
  assert.equal(windowFor('Claude-Sonnet-4-6', 'anthropic', known), 1_000_000);
  // A prefixed id the catalog lists only under another prefix still resolves by bare name.
  assert.equal(windowFor('claude/claude-sonnet-4-6', undefined, known), 1_000_000);
  // Nothing in the catalog: the table answers as before.
  assert.equal(windowFor('gpt-4o-mini', undefined, known), 128_000);
  assert.equal(windowFor('something-unknown', undefined, known), DEFAULT_WINDOW);
  // A model the table has never heard of, sized by the catalog.
  assert.equal(windowFor('zai/glm-5', undefined, known), 204_800);
  assert.equal(windowFor('zai/glm-5'), DEFAULT_WINDOW);
});

test('contextMeter sizes itself to the catalog window when one is known', () => {
  const known = windowIndex([{ id: 'auto/best-coding', contextLength: 1_000_000 }]);
  assert.equal(contextMeter(150_000, 'auto/best-coding').zone, 'danger');
  assert.equal(contextMeter(150_000, 'auto/best-coding', undefined, known).zone, 'ok');
  assert.equal(contextMeter(150_000, 'auto/best-coding', undefined, known).window, 1_000_000);
});
