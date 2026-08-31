import assert from 'node:assert/strict';
import { test } from 'node:test';
import { DEFAULT_WINDOW, contextMeter, meterBar, windowFor } from '../src/ui/modelWindows.js';

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
