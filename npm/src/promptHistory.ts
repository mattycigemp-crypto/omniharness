import { promises as fs } from 'node:fs';
import os from 'node:os';
import path from 'node:path';

const HISTORY_FILE = 'history.json';
const MAX_ENTRIES = 200;

function historyPath(): string {
  const override = process.env.OMNIHARNESS_CONFIG_DIR;
  const base = override && override.trim() !== ''
    ? override
    : process.platform === 'win32'
      ? (process.env.APPDATA ?? path.join(os.homedir(), 'AppData', 'Roaming'))
      : (process.env.XDG_CONFIG_HOME ?? path.join(os.homedir(), '.config'));
  return path.join(base, 'omniharness', HISTORY_FILE);
}

/** Read persisted prompts, newest first. Empty on any failure. */
export async function loadPromptHistory(): Promise<string[]> {
  try {
    const raw = await fs.readFile(historyPath(), 'utf8');
    const data: unknown = JSON.parse(raw);
    if (!Array.isArray(data)) return [];
    return data.filter((item): item is string => typeof item === 'string' && item.trim() !== '');
  } catch {
    return [];
  }
}

/** Append one prompt (dedup consecutive repeats), trimming to MAX_ENTRIES newest. */
export async function appendPromptHistory(prompt: string): Promise<void> {
  const trimmed = prompt.trim();
  if (trimmed === '') return;
  const history = await loadPromptHistory();
  if (history[0] === trimmed) return;
  const next = [trimmed, ...history.filter((item) => item !== trimmed)].slice(0, MAX_ENTRIES);
  await fs.mkdir(path.dirname(historyPath()), { recursive: true });
  await fs.writeFile(historyPath(), JSON.stringify(next, null, 2), 'utf8');
}
