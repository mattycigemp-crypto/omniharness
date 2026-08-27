import assert from 'node:assert/strict';
import { mkdtemp, rm, writeFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { test } from 'node:test';
import { createSystemTools } from '../src/tools/systemTools.js';

test('filesystem tools stay inside the workspace and index deterministically', async () => {
  const root = await mkdtemp(path.join(os.tmpdir(), 'omniharness-'));
  try {
    await writeFile(path.join(root, 'b.txt'), 'b');
    await writeFile(path.join(root, 'a.txt'), 'a');
    const tools = createSystemTools(root);
    assert.deepEqual((await tools.indexWorkspace.execute()).files.map((file) => file.path), ['a.txt', 'b.txt']);
    await assert.rejects(() => tools.readFile.execute({ path: '../outside.txt' }));
  } finally { await rm(root, { recursive: true, force: true }); }
});

test('shell execution is denied unless explicitly enabled', async () => {
  const root = await mkdtemp(path.join(os.tmpdir(), 'omniharness-'));
  try { await assert.rejects(() => createSystemTools(root).runCommand.execute({ command: 'node', args: ['-e', ''] })); }
  finally { await rm(root, { recursive: true, force: true }); }
});
