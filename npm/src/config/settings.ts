import { promises as fs } from 'node:fs';
import os from 'node:os';
import path from 'node:path';

interface Settings {
  activeCombo?: string;
}

function configDir(): string {
  const override = process.env.OMNIHARNESS_CONFIG_DIR;
  if (override && override.trim() !== '') return override;
  const base = process.platform === 'win32'
    ? (process.env.APPDATA ?? path.join(os.homedir(), 'AppData', 'Roaming'))
    : (process.env.XDG_CONFIG_HOME ?? path.join(os.homedir(), '.config'));
  return path.join(base, 'omniharness');
}

function configPath(): string {
  return path.join(configDir(), 'config.json');
}

async function readSettings(): Promise<Settings> {
  try {
    const raw = await fs.readFile(configPath(), 'utf8');
    const parsed: unknown = JSON.parse(raw);
    if (parsed && typeof parsed === 'object') {
      const value = (parsed as Record<string, unknown>).activeCombo;
      return { activeCombo: typeof value === 'string' && value.trim() !== '' ? value : undefined };
    }
  } catch {
    // missing or invalid config -> no saved combo
  }
  return {};
}

/** The last combo the user selected, for use as the startup default. */
export async function readActiveCombo(): Promise<string | undefined> {
  return (await readSettings()).activeCombo;
}

/** Persist the combo the user chose so future sessions default to it. */
export async function saveActiveCombo(combo: string): Promise<void> {
  const trimmed = combo.trim();
  if (trimmed === '') return;
  const settings = await readSettings();
  settings.activeCombo = trimmed;
  const dir = configDir();
  await fs.mkdir(dir, { recursive: true });
  await fs.writeFile(configPath(), JSON.stringify(settings, null, 2), 'utf8');
}