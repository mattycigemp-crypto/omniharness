package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"omniharness/internal/version"
)

// npmPackage is the published TypeScript CLI. It is a *different program*
// from this binary: the tarball ships cli.cjs and dist/ only, never a compiled
// Go binary, so `npm install -g omniharness-cli` cannot update this executable.
// It is named here only so `update` can point at the right thing.
const npmPackage = "omniharness-cli"

// newUpdateCmd builds the `omniharness update` command. This binary is always
// built from source, so the command reports what is installed and how to
// rebuild; it never tries to update itself.
func newUpdateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "update",
		Short: "Update OmniHarness to the latest release",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runUpdate(cmd.Context())
		},
	}
}

// compareVersions compares dotted numeric versions ("1.2.3" vs "1.10.0");
// returns -1, 0 or 1. Non-numeric segments are ignored, so prerelease tags
// compare equal to their base version.
func compareVersions(a, b string) int {
	ap, bp := parseVersion(a), parseVersion(b)
	for i := 0; i < len(ap) || i < len(bp); i++ {
		var av, bv int
		if i < len(ap) {
			av = ap[i]
		}
		if i < len(bp) {
			bv = bp[i]
		}
		if av < bv {
			return -1
		}
		if av > bv {
			return 1
		}
	}
	return 0
}

func parseVersion(v string) []int {
	parts := strings.FieldsFunc(v, func(r rune) bool { return r == '.' || r == '-' })
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		n := 0
		for _, ch := range p {
			if ch < '0' || ch > '9' {
				break
			}
			n = n*10 + int(ch-'0')
		}
		out = append(out, n)
	}
	return out
}

// npmLatestVersion returns the latest published version of the npm package.
// It returns ("", nil) when the package is not published yet (npm E404), and
// an error when npm itself is unavailable.
func npmLatestVersion(ctx context.Context) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, "npm", "view", npmPackage, "version").CombinedOutput()
	if err != nil {
		s := string(out)
		if strings.Contains(s, "E404") || strings.Contains(s, "Not found") {
			return "", nil // not published yet
		}
		return "", fmt.Errorf("npm view %s: %w", npmPackage, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// repoRootOf returns the checkout a binary was built into, when it can be
// identified. The convention is <repo>/bin/omniharness, so the grandparent is
// the repo — but only if it actually holds a go.mod. Guessing from the path
// alone printed a `cd` to whatever happened to sit two levels up, which for a
// binary run from a temp directory was a plainly wrong instruction.
func repoRootOf(exe string) (string, bool) {
	if filepath.Base(filepath.Dir(exe)) != "bin" {
		return "", false
	}
	root := filepath.Dir(filepath.Dir(exe))
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		return "", false
	}
	return root, true
}

func runUpdate(ctx context.Context) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate executable: %w", err)
	}
	exe = filepath.Clean(exe)

	fmt.Println("OmniHarness update check")
	fmt.Printf("  current:  %s\n", version.String())
	fmt.Printf("  install:  %s\n", exe)

	// Reported for orientation only. The npm package is the TypeScript CLI, a
	// separate program on its own version line, so its version is never
	// compared against this binary's.
	switch latest, err := npmLatestVersion(ctx); {
	case err != nil:
		fmt.Printf("  npm CLI:  unavailable (%v)\n", err)
	case latest == "":
		fmt.Printf("  npm CLI:  %s is not published yet\n", npmPackage)
	default:
		fmt.Printf("  npm CLI:  %s %s (a separate program)\n", npmPackage, latest)
	}

	fmt.Println("\nThis binary is built from source. To update it:")
	if root, ok := repoRootOf(exe); ok {
		fmt.Printf("  cd %s\n", root)
	}
	fmt.Println("  source scripts/env.sh && go build -o bin/omniharness.exe ./cmd/omniharness")
	fmt.Printf("\nThe npm package installs the TypeScript CLI, not this binary:\n  npm install -g %s@latest\n", npmPackage)
	return nil
}
