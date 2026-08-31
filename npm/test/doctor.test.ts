import assert from 'node:assert/strict';
import { test } from 'node:test';
import { doctor, helpText, maskKey, models, type DoctorClient } from '../src/doctor.js';

const fakeClient = (over: Partial<DoctorClient> = {}): DoctorClient => ({
  endpoint: 'http://127.0.0.1:20128',
  listModels: async () => ['auto/best-coding', 'auto/cheap', 'openai/gpt-5', 'claude/sonnet'],
  listCombos: async () => [{ name: 'daily', strategy: 'balanced' }, { name: 'thinking' }],
  ...over,
});

test('maskKey never leaks the key', () => {
  assert.equal(maskKey(undefined), 'not set');
  assert.equal(maskKey('   '), 'not set');
  assert.equal(maskKey('sk-abcd1234wxyz'), 'key_wxyz');
  assert.equal(maskKey('ab'), 'key_****');
});

test('doctor reports a healthy gateway', async () => {
  const report = await doctor(fakeClient(), { OMNIROUTE_API_KEY: 'sk-live-9999', OMNIROUTE_MGMT_TOKEN: '' });
  assert.equal(report.ok, true);
  const text = report.lines.join('\n');
  assert.match(text, /endpoint {4}http:\/\/127\.0\.0\.1:20128/);
  assert.match(text, /api key {5}key_9999/);
  assert.match(text, /catalog {5}reachable · 4 models \(2 auto\/\*\)/);
  assert.match(text, /combos {6}2 configured · daily, thinking/);
  assert.match(text, /ok — the gateway is reachable/);
  assert.doesNotMatch(text, /sk-live-9999/);
});

test('doctor fails when the catalog is unreachable', async () => {
  const report = await doctor(fakeClient({ listModels: async () => { throw new Error('ECONNREFUSED'); } }), {});
  assert.equal(report.ok, false);
  const text = report.lines.join('\n');
  assert.match(text, /catalog {5}UNREACHABLE · ECONNREFUSED/);
  assert.match(text, /FAILED — could not reach the gateway/);
});

test('models lists combos, then auto/*, then providers', async () => {
  const out = (await models(fakeClient())).join('\n');
  assert.match(out, /your combos\n {2}daily {2}· balanced\n {2}thinking/);
  assert.match(out, /auto engine\n {2}auto\/best-coding\n {2}auto\/cheap/);
  assert.match(out, /providers \(2\)\n {2}claude\/sonnet\n {2}openai\/gpt-5/);
});

test('helpText documents every subcommand and the env vars', () => {
  const help = helpText('1.2.3');
  assert.match(help, /omniharness 1\.2\.3/);
  for (const cmd of ['doctor', 'models', 'update', '--version', '--help']) assert.match(help, new RegExp(`omniharness ${cmd.replace(/[-]/g, '\\-')}`));
  assert.match(help, /OMNIROUTE_URL/);
  assert.match(help, /OMNIROUTE_API_KEY/);
  assert.match(help, /OMNIROUTE_MGMT_TOKEN/);
  assert.match(help, /Shift\+Tab cycles permissions/);
});
