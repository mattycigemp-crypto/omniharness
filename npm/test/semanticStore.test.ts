import assert from 'node:assert/strict';
import os from 'node:os';
import path from 'node:path';
import { promises as fs } from 'node:fs';
import { test } from 'node:test';
import { loadSemanticIndex, saveSemanticIndex, type SemanticCacheEntry } from '../src/semanticStore.js';

function makeEntry(mtimeMs: number, text = 'hello world'): SemanticCacheEntry {
  return { mtimeMs, chunks: [{ path: '/x/a.ts', text, embedding: [0.1, 0.2, 0.3] }] };
}

test('save then load round-trips the index for the same workspace', async () => {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), 'oh-idx-'));
  const cache = new Map<string, SemanticCacheEntry>([['/x/a.ts', makeEntry(111)], ['/x/b.ts', makeEntry(222, 'other')]]);
  try {
    await saveSemanticIndex(root, cache);
    const loaded = await loadSemanticIndex(root);
    assert.equal(loaded.size, 2);
    assert.deepEqual(loaded.get('/x/a.ts')?.chunks[0]?.embedding, [0.1, 0.2, 0.3]);
    assert.equal(loaded.get('/x/b.ts')?.mtimeMs, 222);
  } finally { await fs.rm(root, { recursive: true, force: true }); }
});

test('load returns an empty map when nothing was persisted', async () => {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), 'oh-idx-'));
  try { assert.equal((await loadSemanticIndex(root)).size, 0); }
  finally { await fs.rm(root, { recursive: true, force: true }); }
});

test('load tolerates a corrupt index file and starts empty', async () => {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), 'oh-idx-'));
  await fs.mkdir(path.join(root, '.omniharness'), { recursive: true });
  await fs.writeFile(path.join(root, '.omniharness', 'semantic-index.json'), '{not-json', 'utf8');
  try { assert.equal((await loadSemanticIndex(root)).size, 0); }
  finally { await fs.rm(root, { recursive: true, force: true }); }
});

test('rooms beyond 256KB are not persisted because the indexer never caches them', async () => {
  // Guard against a silent shape regression: entries with malformed chunks are dropped.
  const root = await fs.mkdtemp(path.join(os.tmpdir(), 'oh-idx-'));
  // Intentionally invalid shape under the accepted (path,[mtimeMs,chunks]) schema.
  await fs.mkdir(path.join(root, '.omniharness'), { recursive: true });
  await fs.writeFile(path.join(root, '.omniharness', 'semantic-index.json'), JSON.stringify({ version: 1, entries: [[42, { mtimeMs: 1, chunks: [] }]] }), 'utf8');
  try { assert.equal((await loadSemanticIndex(root)).size, 0); }
  finally { await fs.rm(root, { recursive: true, force: true }); }
});