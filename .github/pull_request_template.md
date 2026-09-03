<!--
Delete anything that does not apply. A one-line fix does not need headings.
-->

## What was wrong

<!--
The behaviour, not the diff. "The context meter never moved because it read a
header no release emits" tells a reviewer more than a list of changed files,
which they can see for themselves.
-->

## What this does

## How you know it works

<!--
The useful part. Anything real you saw:

  - output from a real gateway or a real terminal, pasted
  - the test failing before the fix and passing after
  - platforms you actually ran it on

If you fixed a bug, the strongest thing you can say is that you broke it again
and watched the test fail. Several tests in this repository passed against a
deliberately broken implementation before anyone checked.
-->

- [ ] `gofmt -l ./cmd ./internal` prints nothing
- [ ] `go vet ./...` and `go test ./...`
- [ ] `cd npm && npm run typecheck && npm test`
- [ ] Rendered it, if it changes the terminal UI — `tsc` cannot see a value one
      column too wide or a panel drawing the same list twice

## Anything a reviewer should push back on

<!--
Trade-offs you made, things you were unsure about, anything you left out. This
section being empty is fine; it being wrong is expensive.
-->
