package testutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// InitFakeWorkspace makes dir a git repository and leaves an untracked file
// inside it. Tests that pin their workspace here need git status to return
// non-empty output so the evaluator's diff-check always passes — without this,
// the check fails when the host checkout happens to be clean (e.g. pure CI).
func InitFakeWorkspace(t *testing.T, dir string) {
	t.Helper()
	if out, err := exec.Command("git", "init", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init workspace: %v\n%s", err, out)
	}
	marker := filepath.Join(dir, ".omniharness-test-workspace")
	if err := os.WriteFile(marker, []byte("pending\n"), 0o644); err != nil {
		t.Fatalf("write workspace marker: %v", err)
	}
}
