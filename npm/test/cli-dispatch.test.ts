import assert from 'node:assert/strict';
import { execFile } from 'node:child_process';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { test } from 'node:test';
import { promisify } from 'node:util';

const run = promisify(execFile);
const cliPath = path.join(path.dirname(fileURLToPath(import.meta.url)), '..', 'src', 'cli.tsx');

interface Outcome { code: number; stdout: string; stderr: string }

/** Runs the CLI in a child process, as a user's shell would. */
async function cli(...args: string[]): Promise<Outcome> {
  try {
    const { stdout, stderr } = await run(process.execPath, ['--import', 'tsx', cliPath, ...args], {
      // Never inherit a real gateway or key from the developer's shell.
      env: { ...process.env, OMNIROUTE_URL: 'http://127.0.0.1:1', OMNIROUTE_API_KEY: '' },
      timeout: 60_000,
    });
    return { code: 0, stdout, stderr };
  } catch (error) {
    const e = error as { code?: number; stdout?: string; stderr?: string };
    return { code: e.code ?? 1, stdout: e.stdout ?? '', stderr: e.stderr ?? '' };
  }
}

test('an unknown command is an error, not an invitation to open the TUI', async () => {
  // The dispatch used to end in a catch-all `else` that launched the TUI, so a
  // typo opened an interactive session instead of reporting itself — and in a
  // script it hung or failed with no explanation of what was wrong.
  const result = await cli('doctr');
  assert.equal(result.code, 1, 'must exit non-zero');
  assert.match(result.stderr, /unknown command 'doctr'/);
  assert.match(result.stderr, /--help/);
  // It must not have started rendering the interface.
  assert.doesNotMatch(result.stdout, /omniharness \d+\.\d+\.\d+\s*\n\s*the OmniRoute-native/);
});

test('an unknown flag is rejected rather than silently ignored', async () => {
  const result = await cli('--versoin');
  assert.equal(result.code, 1);
  assert.match(result.stderr, /unknown command '--versoin'/);
});

test('the documented commands still work', async () => {
  const version = await cli('--version');
  assert.equal(version.code, 0);
  assert.match(version.stdout, /^omniharness \d+\.\d+\.\d+/);

  const help = await cli('--help');
  assert.equal(help.code, 0);
  assert.match(help.stdout, /USAGE/);

  // doctor reports a failure against an unreachable gateway, and says so in
  // its exit code so a script can branch on it.
  const doctor = await cli('doctor');
  assert.equal(doctor.code, 1, 'doctor must exit non-zero when the gateway is down');
  assert.match(doctor.stdout, /FAILED/);
});
