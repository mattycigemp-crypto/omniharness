# omniharness-cli

**OmniHarness** — a local-first agent orchestration harness that sits above
[OmniRoute](https://omniroute.ai). It analyzes tasks, picks a strategy, selects
models, drives capability-based agents, verifies results, repairs failures, and
learns from past outcomes — while OmniRoute stays responsible for routing model
requests to providers.

This package is a thin wrapper around the self-contained OmniHarness binary;
the `omniharness` command is the binary itself.

## Install

```sh
npm install -g omniharness-cli
```

## Quick start

```sh
omniharness doctor          # verifies endpoint + auth status, safely
omniharness run "add a README" --headless   # runs in the current directory
omniharness                 # interactive cockpit (TUI)
omniharness sessions        # persisted sessions
omniharness models          # provider/model catalog from OmniRoute
```

The harness treats the **current working directory** as the workspace.

## Configuration

| Variable | Meaning |
|---|---|
| `OMNIROUTE_URL` | OmniRoute endpoint (default `http://127.0.0.1:20128`, the HTTP API port) |
| `OMNIROUTE_API_KEY` | OmniRoute API key — `Authorization: Bearer <key>` on every request |

If `OMNIROUTE_API_KEY` is unset, the harness asks you to paste the key on
interactive launch and holds it **in memory only** — it is never written to
config, sessions, logs, or telemetry. The key is redacted from all output;
`doctor` masks it as `key_<last4>`.

## Notes

- Only `win32-x64` is shipped in this version; other platforms can be added by
  building the Go binary and publishing a new release.
- The source repository is the OmniHarness project; build with
  `scripts/release-npm.sh` and publish with `npm publish`.
