import assert from 'node:assert/strict';
import { test } from 'node:test';
import { palette, supportsTrueColor } from '../src/ui/palette.js';

test('truecolor is detected via COLORTERM', () => {
  assert.equal(supportsTrueColor({ COLORTERM: 'truecolor' }), true);
  assert.equal(supportsTrueColor({ COLORTERM: '24bit' }), true);
  assert.equal(supportsTrueColor({ COLORTERM: '256color' }), false);
  assert.equal(supportsTrueColor({}), false);
});

test('truecolor palette resolves hex colors', () => {
  const p = palette({ COLORTERM: 'truecolor' });
  assert.match(p.accent, /^#/);
  assert.deepEqual(Object.keys(p).sort(), ['accent', 'error', 'info', 'muted', 'success', 'warn']);
});

test('ANSI fallback uses named colors when COLORTERM is unsupported', () => {
  const p = palette({});
  assert.equal(p.accent, 'blue');
  assert.equal(p.success, 'green');
  assert.equal(p.error, 'red');
  assert.equal(p.info, 'cyan');
});