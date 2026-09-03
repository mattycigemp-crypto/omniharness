# Contributing

Two programs live here. The Go tree (`cmd/`, `internal/`) is the orchestration
engine and the scriptable CLI. `npm/` is a separate TypeScript program — the
terminal UI, published as `omniharness-cli`. They share a version line and a
gateway, not a codebase.

Everything routes through OmniRoute. `internal/gateway` is the only place that
knows how to talk to it; nothing else should grow an HTTP client.

## Getting it running

You need Go 1.27 and Node 24. A gateway helps but is not required — most of the
suite runs against fakes.

```bash
go build ./cmd/omniharness          # Go CLI
cd npm && npm ci && npm run build   # terminal UI
```

`omniharness doctor` tells you what it can and cannot reach. Two failing checks
about the endpoint just mean OmniRoute is not running; `go` and `git` being
absent are warnings, not failures, because a prebuilt binary needs neither.

## The gate

CI runs these and they are required on `main`. Run them before opening a PR.

```bash
gofmt -l ./cmd ./internal          # must print nothing
go vet ./...
go test ./...
cd npm && npm run typecheck && npm test
```

Two things worth knowing about it:

`go test -race` only runs in CI. The detector needs cgo, which a Windows box
without gcc does not have, and this harness is concurrent enough that the
detector is the only thing that finds a race reliably.

`npm run typecheck` covers the tests as well as `src`. It did not always, and
what that cost was a test that passed an option object as `true`, silently took
the default, and asserted against the wrong screen for a week.

## Tests

The bar is not coverage. It is whether the test fails when the behaviour is
wrong.

If you fix something, break it again afterwards and watch the test fail. Most of
the bugs found in this repository were found that way, and more than one test
written here has passed against a deliberately broken implementation — a width
assertion that could not fail because the terminal wraps rather than overflows,
a status-marker check that pinned a fabricated number as correct. A test you
have not seen fail is a guess.

Two habits that follow from that:

**Do not test what the machine you are on happens to have.** Plugin discovery
reads `~/.claude/plugins`, so the suite pins `OMNIHARNESS_PLUGIN_PATH` empty.
Verification reads the ambient git tree, so tests pin a workspace. A suite that
passes only on your laptop is worse than one that fails on everyone's.

**Render the terminal UI before believing it.** `tsc` cannot see that a value
is one column too wide, that a label has run into its own text, or that a panel
has drawn the same list twice. Every one of those shipped past a green suite.

## Commits and releases

Conventional commits — `feat(scope):`, `fix(scope):`, `test(scope):`. The
release notes are built from the subject lines, so write them to be read by
somebody deciding whether to upgrade.

Merging to `main` publishes. The version is computed, npm and the GitHub release
are cut from the same number, and six binaries are cross-compiled from one
runner. Changes to docs, the landing page, `.github/`, and `_test.go` files skip
the release, because a version that installs identical code buys nobody
anything.

Merge style is rebase; history stays linear.

## Cross-platform

CI runs the suite on Linux, macOS and Windows. That job exists because it was
added and immediately failed on both new platforms: workspace confinement
compared a resolved path against an unresolved root, so on macOS — where `/var`
is a symlink to `/private/var` — the harness could not read or write a single
file in a temp workspace. It had shipped in every release until then.

Path handling, symlink resolution, process termination and file locking all
differ. If you touch any of them, assume you are wrong about at least one
platform until CI says otherwise.

## Style

Match the file you are in. Beyond that:

Write comments about **why**, and only where the reason is not evident. A
comment restating the code is noise; a comment explaining why the obvious
approach was wrong is the most valuable line on the screen. Several functions
here carry a note about the bug that shaped them — keep those alive, and add to
them when you learn something the hard way.

The interface avoids decoration. Plain words rather than glyphs (`ok`, `FAIL`,
`no`), an ASCII spinner, terse lowercase copy, no gradients and no exclamation
marks. The CLI follows [clig.dev](https://clig.dev).

Never show a number nobody computed. If a figure is not measured, leave the row
out rather than print a zero — `$0` reads as "this run was free", not as "not
tracked". Sections are absent rather than empty for the same reason: a heading
with nothing under it reads as something failing to load.

## Opening a PR

Say what was wrong, not just what changed. If you verified something against a
real gateway or a real terminal, say so and paste what you saw — that is worth
more than a description of the diff.

CI on a first PR from a fork waits for a maintainer to approve the workflow run.
That is GitHub's default, not a comment on your patch.
