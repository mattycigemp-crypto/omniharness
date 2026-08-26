# OmniHarness — Architecture & OmniRoute Integration Report

Status: **Phase 0 complete; implementation in progress**

## 1. Environment findings (Phase 0)

- The workspace (`/c/AI/omniharness`) is empty. **No OmniRoute source repository exists
  anywhere on this machine.**
- OmniRoute exists as a **local runtime** at `~/.omniroute`:
  - `storage.sqlite` (encrypted at rest), `call_logs/<date>/*.json`, `logs/application/app.log`
  - HTTP routing/proxy server (Anthropic-style + OpenAI-style endpoints), dashboard WS on
    `127.0.0.1:20128` HTTP API / `20131` WebSocket-only listener / `20132` live
    channel (server was not running at inspection time)
- The integration surface was reverse-engineered from 261 real call logs:

| Endpoint | Method | Purpose |
|---|---|---|
| `/chat/completions` | POST | OpenAI-compatible chat completions (stream + non-stream) |
| `/v1/v1/messages` | POST | Anthropic-style `messages` proxied through the gateway |
| `/api/providers/test` | POST | Provider connectivity test |
| `/api/providers/{id}/models` | GET | Model catalog for a provider |

- Model addressing convention: **`provider/model`**, e.g. `cursor/claude-opus-4-8-thinking-xhigh`.
- Providers observed: cursor, openai, claude, gemini, opencode-zen, opencode-go, agnes,
  free-stack, openrouter, kilocode, siliconflow, api-airforce, ollama-local, g4f-ollama,
  agentrouter.
- Toolchain: **Go is not installed** → portable Go 1.27.0 installed at `~/go-sdk/go`
  (user-approved). Host: Windows/AMD64.

## 2. Integration boundary (what OmniHarness must NOT duplicate)

OmniHarness treats OmniRoute as an opaque **provider execution layer**. It never:

- reads or stores provider credentials/API keys
- talks to upstream providers directly
- implements quota, account, or provider translation logic
- routes, retries, or fails over at the provider level

OmniHarness **does**:

- analyze tasks, choose strategies, orchestrate agents
- express *model-selection intent* as `provider/model` + capability requirements
- send OpenAI-compatible chat requests to the configured OmniRoute endpoint
- record outcomes, costs, tokens, and performance memory

The only integration package is `internal/gateway`. Swapping the transport
(OmniRoute gateway → direct provider → in-process stub) is a one-file change, which is
what makes the whole test suite runnable without a live OmniRoute server.

## 3. System topology

```
USER
 │
 ▼
CLI ──────────┐        ┌── TUI (Bubble Tea, thin consumer)
              ▼        ▼
        ┌─────────────────────────────┐
        │      core.Runtime           │  wiring, lifecycle, shutdown
        └─────────────┬───────────────┘
                      ▼  typed events (internal spine)
┌──────────┬──────────┬──────────┬──────────────┬─────────────┐
│ Task     │Strategy  │Orchestr. │ Agent        │ Context     │
│ Analyzer │ Engine   │ (graph)  │ Runtime      │ Composer    │
└────┬─────┴────┬─────┴────┬─────┴──────┬───────┴──────┬──────┘
     │          │         │            │              │
     ▼          ▼         ▼            ▼              ▼
 ┌──────────┐ ┌──────────┐ ┌──────────┐ ┌──────────┐ ┌──────────┐
 │ Budget   │ │ Policy   │ │ Tools +  │ │ Evaluate │ │ Repair   │
 │ Engine   │ │ Engine   │ │ MCP      │ │ Engine   │ │ Engine   │
 └──────────┘ └──────────┘ └────┬─────┘ └────┬─────┘ └────┬─────┘
                                │            │            │
                                ▼            ▼            ▼
                        ┌─────────────────────────────────────┐
                        │        Model Selection Engine       │
                        └──────────────────┬──────────────────┘
                                           ▼  intent: provider/model + capabilities
                        ┌─────────────────────────────────────┐
                        │   gateway.Client → OmniRoute HTTP   │  ← the ONLY boundary
                        └─────────────────────────────────────┘
                                           ▼
                                  ┌──────────────────┐
                                  │    OmniRoute     │  providers, accounts, quota
                                  └──────────────────┘
```

Cross-cutting: `event` (spine), `session` (SQLite durable state), `telemetry`
(recorded from events, never fabricated), `memory` (performance + project).

## 4. Package layout

```
cmd/omniharness        entry point
internal/event         typed event system (spine)
internal/config        TOML config + validation
internal/session       session/task/agent persistence (SQLite, modernc)
internal/task          Task model + Analyzer → TaskProfile
internal/strategy      strategy selection from profile
internal/orchestrator  task-graph scheduler, worker pool, result synthesis
internal/agent         agent runtime, roles, lifecycle state machine
internal/model         capability-based model selection
internal/gateway       OmniRoute OpenAI-compatible client
internal/context       context engine + composer
internal/memory        project + performance memory (SQLite)
internal/tools         tool registry + native tools (fs, shell, git, search, proc)
internal/mcp           MCP stdio client (first-class protocol)
internal/policy        risk classes, permission evaluation, approvals
internal/evaluate      evaluator framework (build/test/lint/constraint/evidence)
internal/repair        failure classification + repair strategies
internal/budget        token/cost/time/agent/tool-call budgets
internal/telemetry     metric recording + aggregation
internal/cli           cobra commands (headless-first)
internal/tui           Bubble Tea cockpit (consumer of events)
internal/combo         model-combo registry (auto/* routing combos + catalog)
internal/version       build info
```

Dependency direction is downward only; `event` and `config` are leaves. No cycles.

## 5. Key decisions

- **Event system is the spine.** Typed envelope (`Event{Type, Data(json.RawMessage)}`),
  fan-out bus with bounded subscriber buffers (drop-oldest, never blocks the runtime),
  full persistence per session for replay/debug.
- **SQLite via `modernc.org/sqlite`** — pure Go, no CGO (no gcc on this host).
- **Strategy selection is a pure function** of `TaskProfile` + budgets + historical
  performance → testable without any runtime.
- **Single-agent is a first-class strategy.** The strategy engine must be able to say
  "one agent, direct" and mean it.
- **Model selection speaks intent**: `Capability` (reasoning/fast/cheap/long-context/
  coding/vision/tools/research/review) → concrete `provider/model` via config + live
  catalog probe of OmniRoute; resolved only at request time.
- **Repair changes variables, never blind-retries** the identical failed execution.
- **No fake telemetry.** Every metric in the TUI comes from recorded events or
  session rows.
- **MCP is first-class**: native client over stdio JSON-RPC 2.0; tools registered into
  the same registry as native tools, so policy applies identically.
- **TUI is a consumer.** All state flows through the runtime event bus; the TUI renders
  and sends control commands (pause/cancel/approve) only.
- **The "stack" is the model combo.** `omniharness stack` (and the TUI `p` picker)
  selects the model combo the harness routes through: an OmniRoute `auto/*` routing
  combo or a specific `provider/model` id. The list comes from the live `/v1/models`
  catalog (`internal/combo` curates it: best-* first, then pro-*, then specific
  models), with a static fallback so the picker works offline. Selection persists to
  `[models] default`.
- **Actual models are always visible.** The TUI aggregates every resolved model used
  in a session (calls, tokens, cost, failures, selection reason) and shows them in the
  sidebar, the routing view, and a live header badge — including multi-model runs
  (repair escalations, role-capability differences).
- **The API key is never written by Save.** `config.Save` (used by `stack set` and the
  TUI picker) scrubs the key before encoding, so an env-provided key can never leak
  into the config file on disk.

## 6. Phases

1. Core runtime (event, config, session, task model) — *in progress*
2. Agent runtime
3. OmniRoute integration (gateway, model selection, budget)
4. Tools + policy + MCP
5. Strategy engine + orchestrator
6. Context + memory
7. Evaluation + repair
8. CLI (headless-first)
9. TUI
10. Benchmark
11. Hardening (concurrency, security, UX, docs, regression)

## 7. OmniRoute authentication (verified from the runtime)

OmniRoute authenticates gateway requests with an API key. The mechanism was
verified directly against the installed OmniRoute server source
(`C:\Users\wegot\AppData\Roaming\npm\node_modules\omniroute`, v3.8.48,
`src/sse/services/auth.ts` and `src/server/authz/policies/clientApi.ts`):

- **Header**: `Authorization: Bearer <key>` on every client request. The
  `x-api-key` header is only honored when the request also carries
  `anthropic-version` (the Anthropic Messages contract); OmniHarness speaks the
  OpenAI-compatible dialect, so it always uses `Authorization: Bearer`.
- **Validation**: presented keys are checked against the `api_keys` table and
  the `OMNIROUTE_API_KEY` / `ROUTER_API_KEY` env passthrough. With
  `REQUIRE_API_KEY` off (default local mode) anonymous requests are allowed;
  with it on, a missing or invalid key yields `401 AUTH_002`.
- **Key format**: `sk-…` (an `omniharness-cli` key is already provisioned in
  this installation's `api_keys` table).

Configuration (never committed, never printed):

```sh
export OMNIROUTE_URL=http://127.0.0.1:20128
export OMNIROUTE_API_KEY=sk-…   # the OmniRoute API key
```

`OMNIHARNESS_ENDPOINT` / `OMNIHARNESS_API_KEY` remain as legacy aliases. The key
is only ever held in memory and sent in the request header; it is redacted from
every error message, masked to `key_<last4>` in `doctor`, and never written to
config files, sessions, telemetry, or logs.

**Global setup is one command.** `source scripts/env.sh` puts the Go toolchain
and the built `bin/omniharness` on PATH (from any directory) and defaults
`OMNIROUTE_URL` when unset; it never sets the API key. For every new terminal,
add that source line to `~/.bashrc` — the script itself never edits your
profile.

Launching the harness (`omniharness` or `omniharness start`) is a single
command: when no key is configured it prompts interactively — `Paste your key
(sk-…):` — accepts the pasted value for the session (never persisted), and
re-validates against the server. Non-interactive invocations (no terminal) get
a clear error naming `OMNIROUTE_API_KEY` instead of prompting.

`omniharness doctor` distinguishes five states: unreachable, auth-not-required
(server accepts anonymous), auth-not-configured (server requires a key, none
set), auth-rejected, and authenticated — plus misconfigured for a reachable
endpoint that misbehaves.

## 8. Hardening pass (hostile review)

- **Memory is a real feedback mechanism** (`internal/memory/advisor.go`): the
  Advisor aggregates recorded outcomes (success rate, repairs, latency, cost)
  into explainable recommendations. Model selection consults it through
  `model.Selector.Empirical` and strategy selection through
  `strategy.Input.History`. Scoring uses the Wilson lower confidence bound +
  a repair penalty; cold start (below `MinRuns=3` per candidate) is
  deterministic — config wins and the reason says so. Every selection carries
  a human-readable reason surfaced in `ModelRequestedData.Reason` and
  `StrategySelectedData.Reason`.
- **Strategy-level repair is wired** (`internal/repair`): verification
  failures classify as build/test/evaluate; attempt 1-2 escalate roles/models,
  and the final attempt restructures execution (direct → sequential →
  plan-implement-verify). The orchestrator applies `ExecutionStrategy`
  overrides and re-publishes `StrategySelected` so repair is observable.
  Repairs stay bounded by `MaxTaskRepairs`.
- **Persistence barrier** (`runtime.FlushEvents`): `RunTask` waits (bounded,
  5s) for the async event sink to drain before returning, so a task is never
  reported complete while its terminal event is still in flight. The task row
  itself is written synchronously either way.
- **Subprocess environment hygiene** (`internal/envguard`): every spawned
  process (shell, git, process tools, evaluators, MCP servers) inherits the
  environment minus `OMNIROUTE_API_KEY` / `OMNIHARNESS_API_KEY` /
  `ROUTER_API_KEY`. MCP servers now also inherit PATH (previously they got an
  empty environment).
- **Tool confinement**: `resolvePath` now follows symlinks (`EvalSymlinks`)
  so a symlink inside the workspace cannot escape it; the git tool rejects
  `-C` / `--git-dir`.
- **Agent transcript protocol fix**: assistant messages carrying `tool_calls`
  are now appended before their tool results — the OpenAI wire format OmniRoute
  serves requires this for multi-turn tool use.
- **Cancellation propagation**: evaluator commands now run under the task
  context instead of `context.Background()`.
- **Store**: writes are serialized through a mutex (no `SQLITE_BUSY` under
  parallel agents); persistence failures surface as bus events instead of
  being silently discarded.
- **TUI**: the `running` flag is set synchronously on submit (blocking
  duplicate tasks, making `q` cancel instead of quit); each task creates
  exactly one session.
- **`serve /health`** reports the real version and an auth-state diagnosis.

## 9. Live integration verification (real OmniRoute, authenticated)

Verified against the running OmniRoute instance (`omniroute serve`, port map
below) with a real model inference:

- **Port map (same server process):** `20128` = HTTP API (`/v1/models` → 200,
  chat completions work); `20131` = WebSocket-only listener that answers `426
  Upgrade Required {"error":"upgrade_required","message":"Use WebSocket."}` to
  plain HTTP; `20132` = live dashboard channel. `OMNIROUTE_URL` must point at
  `20128`; the gateway now classifies a 426 as a WebSocket-only listener and
  prints that hint instead of "misconfigured".
- **Streaming:** OmniRoute streams by default. The gateway always sends
  `"stream":false` (the agent already did), so responses decode as plain JSON.
- **Anonymous local mode:** with `REQUIRE_API_KEY` off, `/v1/models` and
  chat completions work with no key (`doctor` reports `auth not required`).
  `/api/providers` still requires a key → `models` errors with a hint to set
  `OMNIROUTE_API_KEY`.
- **Real inference:** `auto/best-fast` → `gemini-3.1-flash-lite` (streamed),
  `auto/best-coding` → `aion-labs/aion-3.0-mini` → answered `PONG` end to end
  through the harness (`run` → analyze → strategy → agent → model → verify →
  persisted `task.completed`). `cursor/*` models exist in the catalog but do
  not route on this instance (provider not provisioned); `openai/gpt-5.4`
  routes but is out of credits (429 — surfaced as a typed provider error).
- **Config defaults now use `auto/best-*`** so fresh installs work on any
  instance; the home config (`~/.omniharness.toml`) was migrated from
  `cursor/…` + `openai/gpt-5.4`.
- **`/api/providers`** returns `{"connections":[{id, provider, authType,
  isActive, testStatus, providerSpecificData…}]}` (not a bare array). The
  decoder maps only display fields — provider credentials embedded in
  `providerSpecificData` are dropped, never echoed. Per-provider models take
  the connection UUID.
- **Doctor probe budget** raised 10s → 45s: `/v1/models` can take >25s to
  rebuild the 5,886-model catalog after idle, which previously misreported a
  live server as unreachable.
- **`omniharness update`** (`internal/cli/update.go`): checks the npm registry
  for a newer `omniharness-cli` release. npm-installed binaries self-update
  (`npm install -g omniharness-cli@latest`, printed before running);
  source/dev builds print the rebuild command. Version comparison is numeric,
  and an unpublished package (E404) is reported, not treated as an error.
  Launching the harness prints a best-effort, 24h-cached "update available"
  notice for npm installs (source builds are never nagged).
- **Release pipeline**: `.github/workflows/publish.yml` auto-publishes on every
  push to `main` via npm **trusted publishing (OIDC)** — `id-token: write`, no
  npm token, automatic provenance. The shared `scripts/release-npm.sh` bumps
  the version (patch default; `--minor`/`--major`/explicit), verifies with
  `go vet` + the full suite, builds, and publishes; `--dry-run` skips publish.
  A local bypass-2FA-token hook was tried first but abandoned: npm is
  deprecating bypass-2FA tokens (direct publish ends January 2027), and OIDC
  only exists on hosted CI runners.

## 10. Anti-goals (v1)

No Kubernetes, microservices, remote DBs, message brokers, web frontends, Electron,
vector databases, or "AI frameworks". Local-first, native, single binary.
