// Loaded via `node --test --import ./test/setup.mjs`, before any test file and
// therefore before Ink is imported.
//
// Ink asks `is-in-ci` whether it is running in CI, and when it believes it is,
// it stops writing live frames to stdout: it keeps the current frame in memory
// and flushes it only on unmount (see the `isInCi` branches in
// node_modules/ink/build/ink.js). That is sensible for a real CI log, but the
// TUI tests assert on stdout *during* a render — they check what is on screen
// while a run is in flight, then unmount. Under GitHub Actions, which sets
// CI=true, every one of those assertions saw an empty screen: the tests failed
// with nothing but the raw kitty/sync probes in the buffer.
//
// `is-in-ci` treats CI=false and CI=0 as an explicit opt-out, so this restores
// the interactive render path the tests are written against. It affects only
// this test process — nothing about how the published CLI detects CI.
process.env.CI = 'false';
