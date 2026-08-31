import { mkdir, readFile, rm, writeFile } from 'node:fs/promises';
import { join } from 'node:path';
import type { HarnessMessage, TodoItem } from './types/index.js';

export interface StoredSession {
  messages: HarnessMessage[];
  taskQueue: TodoItem[];
  savedAt: string;
}

const SESSION_FILE = 'session.json';

function sessionPath(root: string): string {
  return join(root, '.omniharness', SESSION_FILE);
}

/** Load the previous session; null when none exists or the file is unreadable/corrupt. */
export async function loadSession(root: string): Promise<StoredSession | null> {
  try {
    const raw = await readFile(sessionPath(root), 'utf8');
    const data: unknown = JSON.parse(raw);
    if (typeof data !== 'object' || data === null) return null;
    const messages = (data as { messages?: unknown }).messages;
    if (!Array.isArray(messages)) return null;
    const clean: HarnessMessage[] = [];
    for (const message of messages) {
      const candidate = message as Partial<HarnessMessage>;
      if (
        typeof candidate?.role === 'string' &&
        typeof candidate?.content === 'string' &&
        typeof candidate?.createdAt === 'string'
      ) {
        const base: HarnessMessage = { role: candidate.role as HarnessMessage['role'], content: candidate.content, createdAt: candidate.createdAt };
        if (typeof candidate.model === 'string') base.model = candidate.model;
        if (typeof candidate.toolName === 'string') base.toolName = candidate.toolName;
        clean.push(base);
      }
    }
    const queueRaw = (data as { taskQueue?: unknown }).taskQueue;
    const taskQueue: TodoItem[] = Array.isArray(queueRaw)
      ? queueRaw
          .filter((item): item is TodoItem =>
            typeof (item as TodoItem)?.id === 'string' &&
            typeof (item as TodoItem).title === 'string' &&
            typeof (item as TodoItem).status === 'string')
          .map((item) => ({ id: item.id, title: item.title, status: item.status }))
      : [];
    return { messages: clean, taskQueue, savedAt: String((data as { savedAt?: unknown }).savedAt ?? '') };
  } catch {
    return null;
  }
}

/** Persist the current session (callers treat failures as best-effort). */
export async function saveSession(root: string, session: StoredSession): Promise<void> {
  await mkdir(join(root, '.omniharness'), { recursive: true });
  await writeFile(sessionPath(root), JSON.stringify(session, null, 2), 'utf8');
}

/** Remove the persisted session (used by /clear). */
export async function clearSession(root: string): Promise<void> {
  try {
    await rm(sessionPath(root), { force: true });
  } catch { /* best-effort: no session file to remove */ }
}
