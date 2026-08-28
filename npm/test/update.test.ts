import assert from 'node:assert/strict';
import { test } from 'node:test';
import { compareVersions, ownVersion } from '../src/update.js';

test('compareVersions orders dotted versions numerically', () => {
  const cases: Array<[string, string, number]> = [
    ['0.1.9', '0.1.26', -1],
    ['0.1.26', '0.1.26', 0],
    ['0.2.0', '0.1.99', 1],
    ['1.0.0', '0.9.9', 1],
  ];
  for (const [a, b, expected] of cases) assert.equal(compareVersions(a, b), expected, `${a} vs ${b}`);
});

test('ownVersion reads the installed package manifest', () => {
  assert.match(ownVersion(), /^\d+\.\d+\.\d+/);
});
