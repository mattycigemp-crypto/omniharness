package testutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// InitFakeWorkspace makes dir a git repository with a staged (not committed)
// file. Tests pin their workspace here so the evaluator's diff-check runs
// git status --porcelain and always sees non-empty output. Using a staged file
// (git add) rather than an untracked one avoids being suppressed by
// showUntrackedFiles=no or a global gitignore on CI runners.
func InitFakeWorkspace(t *testing.T, dir string) {
	t.Helper()
	if out, err := exec.Command("git", "-C", dir, "init").CombinedOutput(); err != nil {
		t.Fatalf("git init workspace: %v\n%s", err, out)
	}
	marker := filepath.Join(dir, "workspace.txt")
	if err := os.WriteFile(marker, []byte("pending\n"), 0o644); err != nil {
		t.Fatalf("write workspace marker: %v", err)
	}
	if out, err := exec.Command("git", "-C", dir, "add", "workspace.txt").CombinedOutput(); err != nil {
		t.Fatalf("git add workspace marker: %v\n%s", err, out)
	}
}
