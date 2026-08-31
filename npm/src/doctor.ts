/**
 * `omniharness doctor` / `models` / `help` — the non-TUI subcommands.
 *
 * Pure and injectable: `doctor` and `models` take a structural client plus an
 * env map so they can be exercised without a live gateway.
 */

export interface DoctorClient {
  readonly endpoint: string;
  listModels(): Promise<readonly string[]>;
  listCombos(): Promise<readonly { name: string; strategy?: string }[]>;
}

/** Never print a key: `OMNIROUTE_API_KEY` becomes `key_<last4>` or `not set`. */
export function maskKey(value: string | undefined): string {
  const key = (value ?? '').trim();
  if (key === '') return 'not set';
  return key.length <= 4 ? 'key_****' : `key_${key.slice(-4)}`;
}

export interface DoctorReport {
  ok: boolean;
  lines: readonly string[];
}

/**
 * Check the gateway connection without mutating anything: endpoint, auth
 * status, and whether the model/combo catalog is reachable.
 */
export async function doctor(client: DoctorClient, env: Record<string, string | undefined> = process.env): Promise<DoctorReport> {
  const lines: string[] = [];
  lines.push(`endpoint    ${client.endpoint}`);
  lines.push(`api key     ${maskKey(env.OMNIROUTE_API_KEY)}`);
  lines.push(`mgmt token  ${maskKey(env.OMNIROUTE_MGMT_TOKEN)}${(env.OMNIROUTE_MGMT_TOKEN ?? '').trim() !== '' ? ' — MCP tools enabled' : ''}`);

  let ok = true;
  try {
    const models = await client.listModels();
    const autos = models.filter((id) => id.startsWith('auto/')).length;
    lines.push(`catalog     reachable · ${models.length} models (${autos} auto/*)`);
  } catch (reason: unknown) {
    ok = false;
    lines.push(`catalog     UNREACHABLE · ${reason instanceof Error ? reason.message : String(reason)}`);
  }

  try {
    const combos = await client.listCombos();
    lines.push(`combos      ${combos.length} configured${combos.length > 0 ? ` · ${combos.slice(0, 4).map((c) => c.name).join(', ')}${combos.length > 4 ? ' …' : ''}` : ''}`);
  } catch (reason: unknown) {
    lines.push(`combos      unavailable · ${reason instanceof Error ? reason.message : String(reason)}`);
  }

  lines.push('');
  lines.push(ok ? 'ok — the gateway is reachable.' : 'FAILED — could not reach the gateway. Check OMNIROUTE_URL and that OmniRoute is running.');
  return { ok, lines };
}

/** `omniharness models` — the routing catalog, combos first. */
export async function models(client: DoctorClient): Promise<readonly string[]> {
  const out: string[] = [];
  try {
    const combos = await client.listCombos();
    if (combos.length > 0) {
      out.push('your combos');
      for (const combo of combos) out.push(`  ${combo.name}${combo.strategy ? `  · ${combo.strategy}` : ''}`);
    }
  } catch { /* combos are best-effort */ }
  const ids = await client.listModels();
  const autos = [...new Set(ids.filter((id) => id.startsWith('auto/')))].sort();
  const rest = [...new Set(ids.filter((id) => !id.startsWith('auto/')))].sort();
  if (autos.length > 0) { out.push('auto engine'); for (const id of autos) out.push(`  ${id}`); }
  if (rest.length > 0) { out.push(`providers (${rest.length})`); for (const id of rest) out.push(`  ${id}`); }
  return out;
}

export function helpText(version: string): string {
  return [
    `omniharness ${version} — the agent harness built for OmniRoute`,
    '',
    'USAGE',
    '  omniharness            launch the interactive TUI in the current directory',
    '  omniharness doctor     check the gateway connection and auth, safely',
    '  omniharness models     list the routing catalog (combos + auto/* + providers)',
    '  omniharness update     self-update to the latest npm release',
    '  omniharness --version  print the installed version',
    '  omniharness --help     show this message',
    '',
    'ENVIRONMENT',
    '  OMNIROUTE_URL          gateway endpoint (default http://localhost:20128)',
    '  OMNIROUTE_API_KEY      Authorization: Bearer <key> — held in memory only',
    '  OMNIROUTE_MGMT_TOKEN   management token (manage scope) — enables MCP tool discovery',
    '',
    'In the TUI: Ctrl+E cycles mode (plan · build · research · crazy),',
    'Shift+Tab cycles permissions (manual · accept edits · bypass), /help for the rest.',
  ].join('\n');
}
