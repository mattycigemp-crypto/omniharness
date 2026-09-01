package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"0.1.0", "0.1.0", 0},
		{"0.1.0", "0.1.1", -1},
		{"0.1.1", "0.1.0", 1},
		{"0.1.0", "0.2.0", -1},
		{"1.0.0", "0.9.9", 1},
		{"0.1.10", "0.1.9", 1}, // numeric, not lexicographic
		{"0.1.0", "0.1.0-beta", 0},
		{"1.2.3", "1.2", 1}, // longer wins when prefixes tie
	}
	for _, c := range cases {
		if got := compareVersions(c.a, c.b); got != c.want {
			t.Errorf("compareVersions(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestParseVersionToleratesJunk(t *testing.T) {
	// Should not panic and should produce a comparable number.
	if got := parseVersion("v0.1.0"); got[0] != 0 || got[1] != 1 {
		t.Fatalf("parseVersion(v0.1.0) = %v", got)
	}
}

// repoRootOf must only name a directory it can actually verify. Deriving the
// path from the executable alone printed a `cd` to whatever sat two levels up,
// which for a binary outside a checkout was a wrong instruction.
func TestRepoRootOf(t *testing.T) {
	repo := t.TempDir()
	binDir := filepath.Join(repo, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(binDir, "omniharness.exe")

	// No go.mod yet: the layout looks right but the checkout is unconfirmed.
	if _, ok := repoRootOf(exe); ok {
		t.Fatal("must not claim a repo root without a go.mod")
	}

	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, ok := repoRootOf(exe)
	if !ok || got != repo {
		t.Fatalf("repoRootOf = %q, %v; want %q, true", got, ok, repo)
	}

	// A binary somewhere else entirely resolves to nothing.
	if _, ok := repoRootOf(filepath.Join(t.TempDir(), "omniharness.exe")); ok {
		t.Fatal("a binary outside a bin/ directory has no repo root")
	}
}
