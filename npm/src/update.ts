import { spawnSync } from 'node:child_process';
import { createRequire } from 'node:module';

const PKG = 'omniharness-cli';
// On Windows npm is npm.cmd, so shell resolution is required; the command is a
// single string of fixed literals (no user input) to avoid DEP0190.
const npm = (args: string[], stdio: 'inherit' | 'pipe' = 'pipe'): ReturnType<typeof spawnSync> =>
  spawnSync(`npm ${args.join(' ')}`, { encoding: 'utf8' as const, stdio, shell: process.platform === 'win32' });

/** Version of the running CLI, read from the installed package manifest. */
export function ownVersion(): string {
  const require = createRequire(import.meta.url);
  return require('../package.json').version as string;
}

/** Latest version published on npm, or undefined when the registry is unreachable. */
export function latestVersion(): string | undefined {
  const result = npm(['view', PKG, 'version']);
  const version = typeof result.stdout === 'string' ? result.stdout.trim() : '';
  return result.status === 0 && version !== '' ? version : undefined;
}

/** Numeric dotted-version compare ("0.1.9" < "0.1.26"); non-numeric suffixes are ignored. */
export function compareVersions(a: string, b: string): number {
  const pa = a.split('.').map((part) => Number.parseInt(part, 10) || 0);
  const pb = b.split('.').map((part) => Number.parseInt(part, 10) || 0);
  for (let i = 0; i < Math.max(pa.length, pb.length); i += 1) {
    const diff = (pa[i] ?? 0) - (pb[i] ?? 0);
    if (diff !== 0) return Math.sign(diff);
  }
  return 0;
}

/** `omniharness update` — self-upgrade the global npm package. Returns the exit code. */
export function runUpdate(): number {
  const current = ownVersion();
  console.log(`omniharness ${current} — checking npm for updates…`);
  const latest = latestVersion();
  if (latest === undefined) {
    console.error('✗ could not reach the npm registry — check your connection, or run: npm install -g omniharness-cli@latest');
    return 1;
  }
  if (compareVersions(current, latest) >= 0) {
    console.log(`✓ up to date (${current})`);
    return 0;
  }
  console.log(`updating ${current} → ${latest}…`);
  const install = npm(['install', '-g', `${PKG}@latest`], 'inherit');
  if (install.status !== 0) {
    console.error('✗ update failed — run manually: npm install -g omniharness-cli@latest');
    return install.status ?? 1;
  }
  console.log(`✓ updated to ${latest} — restart omniharness`);
  return 0;
}
