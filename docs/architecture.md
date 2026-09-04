# OmniHarness — Architecture & OmniRoute Integration Report

Status: **shipping.** The harness is built and published to npm as
`omniharness-cli`; the sections below are the design of record, and section 1
is kept as the original Phase 0 survey that the design was derived from.

## 1. Environment findings (Phase 0, historical)

*Recorded before any code was written. Kept because the integration surface below
was reverse-engineered here and still describes how OmniRoute is addressed; the
statements about the workspace are no longer true of this repository.*

- There was no OmniRoute **source** repository on the machine — only the runtime.
- OmniRoute runs as a **local runtime** at `~/.omniroute`:
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
- Toolchain: Go 1.27. `scripts/env.sh` puts a portable toolchain at `~/go-sdk`
  on PATH for hosts without a system Go.

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
internal/tools         tool registry + native tools (fs, shell, git, search, proc, memory, replan)
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
- **SQLite via `modernc.org/sqlite`** — pure Go, no CGO, so the build needs no C
  toolchain on any platform.
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

All eleven landed; each maps to a package under `internal/` with its own tests.

1. Core runtime (event, config, session, task model)
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

The terminal UI users actually run is the TypeScript one under `npm/`; the Go
`internal/tui` cockpit is the original and still builds.

## 7. OmniRoute authentication (verified from the runtime)

OmniRoute authenticates gateway requests with an API key. The mechanism was
verified directly against the installed OmniRoute server source
(the installed `omniroute` npm package, v3.8.48,
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
  empty environment). Third-party credentials (`GITHUB_TOKEN`,
  `AWS_SECRET_ACCESS_KEY`, `NPM_TOKEN`) are inherited by default, because an
  agent asked to open a PR or publish a package needs them; a deployment that
  does not want that names them in `policy.secret_env` and they are stripped
  too. The built-in list cannot be turned off from config.
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

## 9. Task intelligence, memory, and mid-run replanning

- **Deep analysis is optional and off by default** (`internal/task/intel.go`,
  `task.DeepAnalyzer`). `Analyzer.Analyze` stays pure and free; a second pass
  spends one model call to produce concrete `AcceptanceCriteria` for a task,
  gated on `Profile.worthDeepening()` (complexity != LOW, or ambiguity/risk
  == HIGH) so trivial tasks never pay for it, and on config
  (`[task] deep_analysis`, default `false`) so no existing install's spend
  changes on upgrade. Every failure path — no gateway configured, a model
  error, unparseable output — falls back to the Profile the pure analyzer
  already produced. The criteria are consumed in two places: the composed
  system prompt carries them to every role (the implementer works toward
  them; a reviewer on a verify step checks against them), and an
  `acceptance-criteria` evaluator records them into the verification record.
  That evaluator never returns Pass or Fail — whether prose criteria are met
  is not something it can measure from a string, and matching keywords would
  manufacture a verdict rather than report one — so it reports NEEDS_REVIEW
  and names them, the same way a required verification with no matching
  evaluator is recorded rather than silently treated as verified.
- **Two heuristic signals that were computed and silently discarded are now
  wired in.** `Profile.Ambiguity` forces `plan-implement-verify` regardless
  of complexity (`internal/strategy/strategy.go`) — a short, vague request
  ("clean up the code") used to go straight to `direct` with no stated plan
  for anyone to check. `Profile.ApprovalRecommended` (Risk == HIGH) now
  actually asks before a high-risk task runs at all
  (`Orchestrator.requestTaskApproval`), through a genuine task-level policy
  method (`policy.Engine.EvaluateAndExecuteTaskRisk`) rather than a repurposed
  tool-call request — the same `[policy.risk_action]` config and the same
  CLI/TUI approver prompt govern both. A decline lands the task `cancelled`,
  not `failed`.
- **Project memory is wired end to end.** `internal/memory.ProjectMemories`
  (backed by a real session-store table) had a complete write/read API and
  zero callers; `composer.Input.ProjectInstructions` already rendered into
  every system prompt and was always `nil`. A `remember` tool (native,
  `RiskLow`, present only when memory is configured) lets an agent persist a
  convention, gotcha, or decision keyed by workspace + a slot name (reusing a
  slot overwrites it — an upsert, not a list); the orchestrator recalls
  what is relevant once per agent construction.
- **Recall is ranked and capped, not wholesale** (`memory.Relevant`).
  Every note ever remembered used to go into every agent's system prompt on
  every task — fine at five notes, actively harmful at fifty, and paid for
  on every model call of every step. Notes are now ranked against the task's
  own prompt and capped (`memory.DefaultRecallLimit`), and when anything is
  held back the prompt says so rather than silently forgetting it. The
  ranking is lexical — shared terms, with a note's `kind` weighted higher —
  and deliberately not embedding-based: vector databases are an explicit
  anti-goal (§11), and a local, dependency-free ranking that is obviously
  right most of the time beats a semantic one needing an embedding endpoint,
  a store and a migration to be right slightly more often. Because the
  scoring is crude it never lets a low score exclude a note while capacity
  remains; notes that share no vocabulary with the task fill the rest of the
  cap in their existing order.
- **A step's own output now reaches the steps that depend on it**
  (`internal/orchestrator/orchestrator.go`, `formatDepOutputs`).
  `strategy.Step.Depends` only ever controlled scheduling order — an "impl"
  step that depends on a "plan" step ran strictly after it but never saw
  what the plan said; `parallel`'s own "join" step, whose task is
  "integrate results", never received any of the parallel sub-tasks'
  results either. Every multi-step strategy now threads each step's direct
  dependencies' output into its prompt (capped per dependency, snapshotted
  under the scheduler's existing lock — no new data race).
- **A step can ask for the task to be restructured without failing first**
  (`request_replan` tool + `repair.Engine`'s `"replan"` failure kind).
  Repair previously triggered only on failure; there was no way for a step
  that succeeded, but found the task needed more structure than planned, to
  say so. The request is collected once the whole plan finishes (steps are
  never aborted mid-flight) and drives the same re-selection/restructuring
  machinery a verification failure uses, skipping the debugger-first
  sequence build/test/evaluate failures get — there is nothing to
  reproduce — and bounded by the same `MaxTaskRepairs` limit.
- **Verification covers Go, Node, Rust and Python, plus lint.** Alongside
  the Go pair: `npm-build`/`npm-test`, `cargo-build`/`cargo-test`, `pytest`,
  and two lint-class checks (`go-vet`, `npm-lint`). Every one is offered to
  every software task and self-skips — on its own project marker (`go.mod`,
  `package.json`, `Cargo.toml`, `pyproject.toml` and friends), on the script
  it needs, and on its own toolchain being installed — so an ecosystem the
  harness cannot check reports NEEDS_REVIEW rather than a failure. Two
  details that are judgment, not mechanism: pytest's exit status 5 ("no
  tests collected") is not a failing suite, and the lint checks report
  PASS_WITH_WARNINGS rather than FAIL, because a finding that predated the
  task should be surfaced on the run without failing every task run against
  that repository. Outcomes aggregate worst-wins, so a warning never masks a
  real failure.
- **Workspace confinement judged the path the model actually sent.**
  policy.Evaluate sees a tool's raw arguments, before the tools layer
  resolves them, and a model normally sends a relative path. The literal
  prefix check treated every relative path as an escape, so with a workspace
  root configured — which the runtime always sets — every write_file that
  did not spell out an absolute path was blocked. The whole test suite
  missed it because the fixtures leave WorkspaceRoot empty and never reach
  that branch; a live run against a real gateway found it in one task.
  Relative paths are now resolved against the root before the containment
  check, while a rooted path (including one Windows does not call absolute,
  such as "/etc/passwd" with no drive letter) is still judged where it
  points.
- **A spinning model is stopped instead of left to exhaust the budget.**
  The agent loop's only exits were a reply with no tool calls, the budget,
  the iteration ceiling, and cancellation — so a model that kept re-issuing
  one identical `write_file` never hit any of them, and the same live run
  burned its whole duration allowance and reported a budget failure for a
  task that had actually succeeded on the first call. The loop now counts
  *immediately consecutive* identical calls (name plus canonicalised
  arguments); the third is answered with an observation saying so rather
  than executed, and a run that ignores it stops with a stall reason repair
  can act on. Only consecutive repeats count, which is what makes it safe:
  re-running `go test` after an edit has the edit between the two runs, so
  it is never blocked, while two identical calls back to back cannot have
  observed anything new. Measured against the live gateway on the same task,
  before and after: the unguarded run executed twenty-odd consecutive
  `write_file` calls, one every ~16s, until the budget ended it; the guarded
  run executed two and then stopped writing, with the file correct on disk.
- **Nor is a wander that never repeats itself consecutively.** The same guarded
  run then went somewhere the consecutive rule cannot reach: the repair agent
  alternated `list_dir` and `find_files` for eight minutes. Reading its
  transcript, 25 calls returned three distinct results between them — the same
  directory listing nine times, the same two globs six and four times, plus a
  repeated argument error and a repeated policy denial — and no two adjacent
  calls were ever identical. So the guard also counts call/result pairs across
  the whole run: a third identical result is replaced with an observation
  saying the model already has it, and ten calls in a row that return nothing
  new stop the run. Keying on the result is what makes this safe, and it is an
  empirical test rather than a positional one: re-running `go test` after an
  edit produces different output, so it is never a repeat, and repeats
  interleaved with genuine progress reset the streak and never stop a run that
  is getting somewhere.
- **What the wander was actually trying to do** is worth recording, because it
  was not confusion: its first act was `shell: cat hello.txt`, to check the
  file it had been asked to verify, and policy denied it. Everything after was
  a search for another way to see the same thing. `read_file` was allowed the
  whole time and never tried.
- **A denied tool no longer prints as a success.** Every tool outcome
  arrives as ToolCompleted with the result in Status; the headless printer
  ignored Status and said "tool ok" for all of them, so the same live run
  showed eight policy denials as completed work with no file on disk.
- **An evaluator never reports a failure it cannot substantiate.** Two
  places used to. A missing toolchain (`go` absent on a machine holding a Go
  repository) surfaced as "build failed: executable file not found" — the
  operator's environment reported as a broken result, on every task. Every
  toolchain-backed evaluator now skips instead. And the research evidence
  check, a substring scan for URLs and citation markers, returned a hard
  FAIL when it found none: that failed honest answers citing the workspace
  rather than the web (what local research produces) into real repair spend,
  while passing anything containing the word "source". It now warns and says
  what it cannot determine. This is the same rule the acceptance-criteria
  evaluator is built on: a fabricated verdict is worse than an honest
  "nobody checked".
- The original Go/npm pair: `NpmBuildEvaluator` /
  `NpmTestEvaluator` (`internal/evaluate/evaluate.go`) mirror
  `GoBuildEvaluator`/`GoTestEvaluator`'s own pattern exactly: offered to
  every software task, each self-skips (`NEEDS_REVIEW`, not a failure) when
  there's no `package.json` or no matching script.
- **A double-counting bug in performance memory is fixed.**
  `memory.Advisor.modelStats` and `StrategyPerformance` joined `tasks`
  straight to `model_calls` and aggregated over that join — a task with
  several calls (the normal case: multiple agent turns, a multi-step
  strategy, a repair cycle) contributed once per call, not once per task,
  which could push a computed success rate mathematically past 100%. Fixed
  by aggregating over a derived table that groups by task id first. This
  data drives real decisions (`RecommendModel`, `RecommendStrategy`) and is
  now also surfaced directly — a **BY STRATEGY** section in
  `omniharness stats`, sourced from the new `telemetry.ByStrategy`.
- **`doctor` reads OmniRoute's own routing-quality snapshot**
  (`gateway.Client.ExplainRouting`, `GET /v1/explain/routing`): a warning,
  not a failure, naming how many tracked models are degraded — quietly
  skipped on a gateway that predates the endpoint (404 reads as "not
  supported," not an error).

## 10. Live integration verification (real OmniRoute, authenticated)

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

## 11. Anti-goals (v1)

No Kubernetes, microservices, remote DBs, message brokers, web frontends, Electron,
vector databases, or "AI frameworks". Local-first, native, single binary.
