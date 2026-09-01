package testutil

import "testing"

// InitFakeWorkspace prepares dir as an isolated test workspace. The
// diff-check evaluator skips when go.mod is absent (same rule as go-build
// and go-test), so no git setup is needed; this helper is kept as a hook
// for any future workspace initialisation that tests require.
func InitFakeWorkspace(t *testing.T, dir string) {
	t.Helper()
	// dir is already created by t.TempDir(); nothing else needed today.
	_ = dir
}
