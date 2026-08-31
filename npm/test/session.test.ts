import assert from 'node:assert/strict';
import os from 'node:os';
import path from 'node:path';
import { promises as fs } from 'node:fs';
import { test } from 'node:test';
import { loadPromptHistory, appendPromptHistory } from '../src/promptHistory.js';
import { clearSession, loadSession, saveSession } from '../src/sessionStore.js';

// Isolate the config dir so tests never touch the developer's real history.
const origDir = process.env.OMNIHARNESS_CONFIG_DIR;
const configDir = path.join(os.tmpdir(), `oh-config-${Date.now()}`);
process.env.OMNIHARNESS_CONFIG_DIR = configDir;

test.after(() => {
  if (origDir === undefined) delete process.env.OMNIHARNESS_CONFIG_DIR;
  else process.env.OMNIHARNESS_CONFIG_DIR = origDir;
});

test('prompt history persists submitted prompts, newest first, deduped and capped', async () => {
  await appendPromptHistory('hello');
  await appendPromptHistory('fix the bug');
  await appendPromptHistory('hello'); // moved to front, not duplicated
  const history = await loadPromptHistory();
  assert.deepEqual(history, ['hello', 'fix the bug']);
});

test('prompt history ignores blank prompts and survives a corrupt file', async () => {
  await appendPromptHistory('   '); // blank -> no-op
  const before = await loadPromptHistory();
  assert.ok(Array.isArray(before));
  assert.ok(!before.includes('   '), 'blank prompt never saved');
  await fs.writeFile(path.join(configDir, 'omniharness', 'history.json'), 'not-json', 'utf8');
  assert.deepEqual(await loadPromptHistory(), []);
});

test('session store round-trips the transcript and task queue', async () => {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), 'oh-session-'));
  const session = {
    messages: [
      { role: 'user' as const, content: 'hi', createdAt: '2026-01-01T00:00:00Z' },
      { role: 'assistant' as const, content: 'hello', model: 'auto/best-coding', createdAt: '2026-01-01T00:00:01Z' },
    ],
    taskQueue: [{ id: 't1', title: 'run tests', status: 'pending' as const }],
    savedAt: '2026-01-01T00:00:02Z',
  };
  await saveSession(root, session);
  const loaded = await loadSession(root);
  assert.deepEqual(loaded?.messages, session.messages);
  assert.deepEqual(loaded?.taskQueue, session.taskQueue);
  await fs.rm(root, { recursive: true, force: true });
});

test('session store: missing file loads null and clearSession resets it', async () => {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), 'oh-session2-'));
  assert.equal(await loadSession(root), null);
  await clearSession(root);
  const after = await loadSession(root);
  assert.equal(after, null);
  await fs.rm(root, { recursive: true, force: true });
});

test('session store ignores malformed messages and queues', async () => {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), 'oh-session3-'));
  const corrupt = { messages: ['bad', { role: 'user', content: 'ok', createdAt: 'x' }], taskQueue: [{ id: 1 }] };
  await saveSession(root, corrupt as never);
  const loaded = await loadSession(root);
  assert.deepEqual(loaded?.messages, [{ role: 'user', content: 'ok', createdAt: 'x' }]);
  assert.deepEqual(loaded?.taskQueue, []);
  await fs.rm(root, { recursive: true, force: true });
});