import { mkdir, readFile, writeFile } from 'node:fs/promises';
import { join } from 'node:path';
import type { IndexedChunk } from './search.js';

export interface SemanticCacheEntry {
  mtimeMs: number;
  chunks: IndexedChunk[];
}

const INDEX_FILE = 'semantic-index.json';

function storePath(root: string): string {
  return join(root, '.omniharness', INDEX_FILE);
}

/** Load a previously persisted index; an empty map when none exists or it is corrupt. */
export async function loadSemanticIndex(root: string): Promise<Map<string, SemanticCacheEntry>> {
  const map = new Map<string, SemanticCacheEntry>();
  try {
    const raw = await readFile(storePath(root), 'utf8');
    const data: unknown = JSON.parse(raw);
    if (typeof data !== 'object' || data === null) return map;
    const entries = (data as { entries?: unknown }).entries;
    if (!Array.isArray(entries)) return map;
    for (const item of entries) {
      if (!Array.isArray(item) || item.length !== 2) continue;
      const [path, entry] = item as [unknown, unknown];
      if (typeof path !== 'string' || typeof entry !== 'object' || entry === null) continue;
      const { mtimeMs, chunks } = entry as { mtimeMs?: unknown; chunks?: unknown };
      if (typeof mtimeMs !== 'number' || !Array.isArray(chunks)) continue;
      map.set(path, { mtimeMs, chunks: chunks as IndexedChunk[] });
    }
  } catch { /* no index yet or unreadable → start empty */ }
  return map;
}

/** Persist the current index (best-effort; callers swallow failures). */
export async function saveSemanticIndex(root: string, cache: Map<string, SemanticCacheEntry>): Promise<void> {
  if (cache.size === 0) return;
  const data = JSON.stringify({ version: 1, entries: [...cache.entries()] });
  await mkdir(join(root, '.omniharness'), { recursive: true });
  await writeFile(storePath(root), data, 'utf8');
}