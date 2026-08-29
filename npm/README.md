# omniharness-cli

**OmniHarness** — a local-first agent orchestration harness that sits above
[OmniRoute](https://omniroute.ai). It analyzes tasks, picks a strategy, selects
models, drives capability-based agents, verifies results, repairs failures, and
learns from past outcomes — while OmniRoute stays responsible for routing model
requests to providers.

This package installs the TypeScript/Ink OmniHarness terminal UI; the `omniharness` command launches the bundled CLI.

## Install

```sh
npm install -g omniharness-cli
# If PowerShell still cannot find it, open a new terminal so npm's global bin
# directory is reloaded into PATH.
```

## Quick start

```sh
omniharness doctor          # verifies endpoint + auth status, safely
omniharness run "add a README" --headless   # runs in the current directory
omniharness                 # interactive cockpit (TUI)
omniharness sessions        # persisted sessions
omniharness models          # provider/model catalog from OmniRoute
omniharness update          # check npm and self-update to the latest release
```

The harness treats the **current working directory** as the workspace.

## Configuration

| Variable | Meaning |
|---|---|
| `OMNIROUTE_URL` | OmniRoute endpoint (default `http://127.0.0.1:20128`, the HTTP API port) |
| `OMNIROUTE_API_KEY` | OmniRoute API key — `Authorization: Bearer <key>` on every request |
| `OMNIROUTE_MGMT_TOKEN` | OmniRoute management token (`manage` scope) — when set, OmniRoute's MCP tools are discovered and exposed to the agent |

If `OMNIROUTE_API_KEY` is unset, the harness asks you to paste the key on
interactive launch and holds it **in memory only** — it is never written to
config, sessions, logs, or telemetry. The key is redacted from all output;
`doctor` masks it as `key_<last4>`.

## Notes

- Node.js 20 or newer is required to run the CLI.
- Publishing is automatic: every push to `main` on GitHub runs
  `.github/workflows/publish.yml`, which bumps the version, verifies
  (vet + tests), builds, and publishes via **npm trusted publishing (OIDC)**
  — no npm token is stored anywhere, and provenance is attached automatically.
  Locally, `scripts/release-npm.sh` (or `scripts\release-npm.ps1`) does the
  same; add `--dry-run` to skip the publish, or pass `--minor`/`--major`/an
  explicit version to control the bump.
