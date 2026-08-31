/**
 * Named session snapshots on top of the single-session store.
 *
 * Snapshots live in `<root>/.omniharness/sessions/<name>.json` with the same
 * StoredSession shape. The active session stays at the legacy `session.json`
 * path so resume-on-startup is unchanged; `/sessions` lists named snapshots
 * and can restore one into the active slot.
 */

import { mkdir, readdir, readFile, rm, writeFile } from 'node:fs/promises';
import { join } from 'node:path';
import type { StoredSession } from './sessionStore.js';

const SESSIONS_DIR = 'sessions';

function sessionsDir(root: string): string {
  return join(root, '.omniharness', SESSIONS_DIR);
}

function snapshotPath(root: string, name: string): string {
  // Names are filesystem-safe: letters, digits, dash, underscore, dot (no path parts).
  const safe = name.replace(/[^A-Za-z0-9._-]/g, '_');
  return join(sessionsDir(root), `${safe}.json`);
}

/** List saved snapshot names (newest mtime first); empty when none exist. */
export async function listSessions(root: string): Promise<{ name: string; savedAt: string }[]> {
  try {
    const entries = await readdir(sessionsDir(root));
    const out: { name: string; savedAt: string; mtime: number }[] = [];
    for (const entry of entries) {
      if (!entry.endsWith('.json')) continue;
      try {
        const raw = await readFile(join(sessionsDir(root), entry), 'utf8');
        const data = JSON.parse(raw) as { savedAt?: unknown };
        const info = await (await import('node:fs/promises')).stat(join(sessionsDir(root), entry));
        out.push({ name: entry.slice(0, -5), savedAt: String(data.savedAt ?? ''), mtime: info.mtimeMs });
      } catch { /* unreadable snapshot: skip */ }
    }
    return out.sort((a, b) => b.mtime - a.mtime).map(({ name, savedAt }) => ({ name, savedAt }));
  } catch {
    return [];
  }
}

/** Save the current session under a snapshot name (overwrites same-name). */
export async function saveSnapshot(root: string, name: string, session: StoredSession): Promise<void> {
  await mkdir(sessionsDir(root), { recursive: true });
  await writeFile(snapshotPath(root, name), JSON.stringify(session, null, 2), 'utf8');
}

/** Load a snapshot by name; null when missing or corrupt. */
export async function loadSnapshot(root: string, name: string): Promise<StoredSession | null> {
  try {
    const raw = await readFile(snapshotPath(root, name), 'utf8');
    const data = JSON.parse(raw) as StoredSession;
    if (!Array.isArray(data.messages)) return null;
    return data;
  } catch {
    return null;
  }
}

/** Delete a snapshot; missing snapshots are a no-op. */
export async function deleteSnapshot(root: string, name: string): Promise<void> {
  try {
    await rm(snapshotPath(root, name), { force: true });
  } catch { /* best-effort */ }
}
