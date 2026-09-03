# AGENTS.md

Instructions for an autonomous coding agent working in this repository — GitHub Copilot coding agent, or any other tool that reads this file. A human contributor should read [CONTRIBUTING.md](CONTRIBUTING.md) instead; it covers the same ground with more of the reasoning and none of the imperative tone.

## What this is

Two programs, one version line. The Go tree (`cmd/`, `internal/`) is the orchestration engine and a scriptable CLI. `npm/` is a separate TypeScript program — the terminal UI, published as `omniharness-cli`. Everything routes through OmniRoute; `internal/gateway` is the only package that talks to it. Do not add an HTTP client anywhere else.

## Build

```bash
go build ./cmd/omniharness
cd npm && npm ci && npm run build
```

## Before you finish any change — run all of this, not a subset

```bash
gofmt -l ./cmd ./internal          # must print nothing
go vet ./...
go test ./...
cd npm && npm run typecheck && npm test
```

`npm run typecheck` covers `test/` as well as `src/` — both must pass. `go test ./...` includes the full suite; the race detector (`-race`) runs in CI, not locally, so a merge can still surface a race this run didn't. Do not consider a change finished because it compiles. It is finished when the gate above passes and you have looked at the actual output, not the exit code alone.

## The one rule about tests that matters more than the others

**A test that has not been watched to fail on the broken version is a guess, not a test.** If you write a test to cover a bug you just fixed, revert the fix and confirm the test fails before you consider the work done. This repository's history has several tests that were wrong in a way a passing run never revealed — an assertion that couldn't fail given how the renderer actually behaves, a status check that happened to accept a fabricated number. Every one was found by deliberately breaking the code and watching the test not notice.

If you are asked to fix a failing test, fix the code. Weakening the assertion so it passes is not a fix, even when the assertion looks wrong to you — say so and ask, or fix the assertion in a way that still fails against the original bug.

## Rules that are not stylistic

- **Never fabricate a number.** If a metric was not actually measured, the UI must show nothing for it rather than a zero. A `$0` reads as "this run was free"; an absent row reads as "not tracked." Both the TUI and the sidebar panel follow this.
- **Never let a subprocess inherit `OMNIROUTE_API_KEY`, `OMNIROUTE_MGMT_TOKEN`, `OMNIHARNESS_API_KEY`, or `ROUTER_API_KEY`.** `internal/envguard` enforces this; do not add a code path that spawns a process without going through it.
- **Do not lower or bypass `policy.RiskAction`, budget ceilings, or the loopback guard in `serve.go`** as a side effect of an unrelated change. If a change legitimately requires touching one of them, say so explicitly in the PR description — this is exactly the kind of change that needs a human's judgment, not an agent's.
- **`shell_allowed = false` must mean no shell**, including by way of another tool. Do not add a tool that can execute arbitrary commands without checking this flag.

See [SECURITY.md](SECURITY.md) for the complete list of what is guaranteed and what is deliberately out of scope — read it before touching `internal/policy`, `internal/envguard`, `internal/budget`, or `internal/cli/serve.go`.

## Terminal UI changes need to actually be rendered

`tsc` cannot see that a value is one column too wide, that a label has run into its own text, or that a panel has drawn the same list twice — all three have shipped past a clean typecheck in this repository. If you change anything under `npm/src/ui/`, render it (see `test/streams.ts` for the fake-terminal harness other tests use) at a few widths before calling the change done, and prefer a test that asserts on the actual rendered frame over one that only checks component props.

## Commits and PRs

Conventional commit prefixes (`feat(scope):`, `fix(scope):`, `test(scope):`) — release notes are built from these subject lines. Say what was wrong and how you verified the fix, not just what changed; a description that says "ran the test, reverted the fix, watched it fail" is worth more to a reviewer than a longer one that doesn't.

Merging to `main` publishes a release. Changes to documentation, `.github/`, or files matching `_test.go` are excluded from cutting one — everything else is not, so do not treat a change as low-stakes just because it looks small.
