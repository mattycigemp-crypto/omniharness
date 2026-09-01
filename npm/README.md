# omniharness-cli

**OmniHarness** — the agent harness built for [OmniRoute](https://omniroute.ai).
Route once, run anywhere: plan, build, research, or turn a swarm loose.

This package ships the interactive **terminal UI** (Ink/React). The orchestration
core and a headless CLI live in the [Go source tree](https://github.com/shipking-ai/omniharness).

```bash
npm install -g omniharness-cli
omniharness            # launch the TUI in the current working directory
```

| Command | |
|---|---|
| `omniharness` | launch the interactive TUI |
| `omniharness doctor` | check the gateway connection + auth, safely (key is masked) |
| `omniharness models` | list the routing catalog — combos, `auto/*`, providers |
| `omniharness update` | self-update to the latest npm release |
| `omniharness --version` / `--help` | version / usage |

Everything else happens inside the TUI.

## Connect to OmniRoute

| Variable | Meaning |
|---|---|
| `OMNIROUTE_URL` | Gateway endpoint (default `http://127.0.0.1:20128`) |
| `OMNIROUTE_API_KEY` | `Authorization: Bearer <key>` on every request — held **in memory only**, never written to config, sessions, logs, or telemetry |
| `OMNIROUTE_MGMT_TOKEN` | Management token (`manage` scope) — when set, OmniRoute's MCP tools are discovered and offered to the agent |

If `OMNIROUTE_API_KEY` is unset, the TUI asks for it on launch and keeps it in
memory only. It is redacted from all output.

## Inside the TUI

- **Modes** (`Ctrl+E` cycles): `plan` · `build` · `research` · `crazy`. Crazy mode
  auto-approves every call and fans an independent plan out across parallel
  worker agents.
- **Native scrollback is the history** — settled turns flow into your terminal's
  own buffer; the full transcript is restored on exit.
- **Route ribbon** — every reply is labelled with the provider it came from;
  failovers appear inline.
- **Context meter**, per-tool cards with diffs, scoped-trust approvals, input
  queued during a run, `Ctrl+Y` clipboard copy over OSC 52, session resume,
  prompt history, `/find`, `/chapters`.

Slash commands: `/help` `/clear` `/sessions` `/save <name>` `/forget <name>`
`/attach <files>` `/find <text>` `/chapters`.

## Notes

- Node.js 20 or newer.
- Publishing is automatic: every push to `main` runs
  `.github/workflows/publish.yml`, which verifies both the Go and TypeScript
  suites, builds, and publishes via **npm trusted publishing (OIDC)** — no token
  is stored, provenance is attached automatically. Locally,
  `scripts/release-npm.sh` does the same (`--dry-run` to skip publish;
  `--minor` / `--major` / an explicit version to control the bump).
