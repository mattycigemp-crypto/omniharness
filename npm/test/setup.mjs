// Loaded via `node --test --import ./test/setup.mjs`, before any test file and
// therefore before Ink is imported.
//
// Ink asks `is-in-ci` whether it is running in CI, and when it believes it is,
// it stops writing live frames to stdout: it keeps the current frame in memory
// and flushes it only on unmount (see the `isInCi` branches in
// node_modules/ink/build/ink.js). That is sensible for a real CI log, but the
// TUI tests assert on stdout *during* a render — they check what is on screen
// while a run is in flight, then unmount. Under GitHub Actions, which sets
// CI=true, every one of those assertions saw an empty screen.
//
// That mode also left the test runner with handles it never released, so
// `node --test` hung until the job's timeout. Both symptoms have the same
// cause, and both go away here: with CI=false the suite renders normally and
// the runner exits on its own, so no --test-force-exit is needed. Keeping the
// runner able to hang is deliberate — a future leak should fail loudly rather
// than be papered over.
//
// `is-in-ci` treats CI=false and CI=0 as an explicit opt-out. This affects only
// this test process — nothing about how the published CLI detects CI.
//
// Reproduce the original failure with: CI=true npm test  (before this file).
process.env.CI = 'false';

// Plugin discovery reaches into ~/.claude/plugins by default, so on a machine
// with plugins installed the engine loaded 61 extra skills and two suites that
// count the agent's tools failed — on that machine only. Tests must not read
// whatever happens to be installed on the box running them, so discovery is
// off unless a test asks for it by passing explicit roots.
process.env.OMNIHARNESS_PLUGIN_PATH = '';
