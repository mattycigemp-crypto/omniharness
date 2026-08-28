import assert from 'node:assert/strict';
import os from 'node:os';
import path from 'node:path';
import { promises as fs } from 'node:fs';
import { beforeEach, afterEach, test } from 'node:test';
import { readActiveCombo, saveActiveCombo } from '../src/config/settings.js';
import { createMastraEngine } from '../src/agent/mastraEngine.js';

let dir: string;
const original = process.env.OMNIHARNESS_CONFIG_DIR;

beforeEach(async () => {
  dir = await fs.mkdtemp(path.join(os.tmpdir(), 'oh-settings-'));
  process.env.OMNIHARNESS_CONFIG_DIR = dir;
});

afterEach(async () => {
  if (original === undefined) delete process.env.OMNIHARNESS_CONFIG_DIR;
  else process.env.OMNIHARNESS_CONFIG_DIR = original;
  await fs.rm(dir, { recursive: true, force: true });
});

test('saved combo round-trips through the user config dir', async () => {
  assert.equal(await readActiveCombo(), undefined);
  await saveActiveCombo('free-stack');
  assert.equal(await readActiveCombo(), 'free-stack');
  // overwrite with a different choice
  await saveActiveCombo('Kimi Coding');
  assert.equal(await readActiveCombo(), 'Kimi Coding');
});

test('blank combo is not persisted', async () => {
  await saveActiveCombo('   ');
  assert.equal(await readActiveCombo(), undefined);
});

test('engine.selectModel awaits persistence so a reload sees the new default', async () => {
  await saveActiveCombo('free-stack');
  const engine = await createMastraEngine({ workspaceRoot: os.tmpdir(), model: await readActiveCombo() });
  assert.equal(engine.state.activeModel, 'free-stack');
  await engine.selectModel('deepseek');
  // Persistence must already be on disk when selectModel resolves.
  assert.equal(await readActiveCombo(), 'deepseek');
});