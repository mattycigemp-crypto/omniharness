import assert from 'node:assert/strict';
import { mkdir, mkdtemp, rm, stat, symlink, writeFile } from 'node:fs/promises';
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

test('a symlink inside the workspace does not let the tools read or write outside it', async (t) => {
  // The lexical check alone is not confinement: a symlink pointing out of the
  // workspace satisfies every string comparison while resolving elsewhere.
  // Repositories carry symlinks routinely, so the agent need not create one.
  const base = await mkdtemp(path.join(os.tmpdir(), 'oh-symlink-'));
  const workspace = path.join(base, 'workspace');
  const outside = path.join(base, 'outside');
  await mkdir(workspace, { recursive: true });
  await mkdir(outside, { recursive: true });
  await writeFile(path.join(outside, 'secret.txt'), 'outside the workspace', 'utf8');

  try {
    await symlink(outside, path.join(workspace, 'escape'), 'junction');
  } catch {
    t.skip('symlinks unavailable on this host');
    return;
  }

  const tools = createSystemTools(workspace, false);

  await assert.rejects(
    () => tools.readFile.execute({ path: path.join('escape', 'secret.txt') } as never),
    /escapes workspace root/,
    'reading through a symlink must be rejected',
  );
  await assert.rejects(
    () => tools.writeFile.execute({ path: path.join('escape', 'planted.txt'), content: 'x' } as never),
    /escapes workspace root/,
    'writing through a symlink must be rejected',
  );
  await assert.rejects(stat(path.join(outside, 'planted.txt')), 'nothing may be created outside');

  // Ordinary paths, including new nested ones, must still work.
  await writeFile(path.join(workspace, 'ok.txt'), 'inside', 'utf8');
  const read = await tools.readFile.execute({ path: 'ok.txt' } as never);
  assert.equal((read as { content: string }).content, 'inside');
  await tools.writeFile.execute({ path: path.join('sub', 'new.txt'), content: 'y' } as never);
});
