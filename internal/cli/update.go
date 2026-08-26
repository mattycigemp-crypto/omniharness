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

// npmPackage is the published wrapper package this binary ships inside.
const npmPackage = "omniharness-cli"

// npmVendorMarker marks a binary installed from the npm package: the package
// ships the platform binary at node_modules/<pkg>/vendor/<os>-<arch>/.
const npmVendorMarker = "node_modules/omniharness-cli/vendor/"

// newUpdateCmd builds the `omniharness update` command. It checks the npm
// registry for a newer omniharness-cli release and self-updates (npm install
// -g) when the binary runs from the npm package; for source/dev builds it
// prints the rebuild command instead.
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

// installedFromNpm reports whether an executable path lives inside the npm
// package's vendor directory (true) or is a source/dev build (false).
func installedFromNpm(exePath string) bool {
	return strings.Contains(filepath.ToSlash(exePath), npmVendorMarker)
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

func runUpdate(ctx context.Context) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate executable: %w", err)
	}
	exe = filepath.Clean(exe)
	fromNpm := installedFromNpm(exe)

	fmt.Println("OmniHarness update check")
	fmt.Printf("  current:  %s\n", version.String())
	fmt.Printf("  install:  %s\n", exe)

	latest, err := npmLatestVersion(ctx)
	switch {
	case err != nil:
		fmt.Printf("  registry: %v\n", err)
	case latest == "":
		fmt.Printf("  registry: %s not published yet — nothing to update from npm\n", npmPackage)
	default:
		fmt.Printf("  registry: latest published %s\n", latest)
	}

	// Only compare when the registry actually reported a version.
	if err == nil && latest != "" {
		switch cmp := compareVersions(version.Version, latest); {
		case cmp == 0:
			fmt.Println("\n✓ up to date")
			return nil
		case cmp > 0:
			fmt.Printf("\nnote: running %s is ahead of the published %s\n", version.Version, latest)
			return nil
		}
	}

	if !fromNpm {
		repoRoot := filepath.Dir(filepath.Dir(exe))
		fmt.Println("\nThis is a source/dev build — update it by rebuilding:")
		fmt.Printf("  cd %s && source scripts/env.sh && go build -o bin/omniharness.exe ./cmd/omniharness\n", repoRoot)
		fmt.Println("(For npm-installed binaries, `omniharness update` self-updates automatically.)")
		return nil
	}

	if err != nil || latest == "" {
		fmt.Printf("\nNothing to update (npm package %s is not published yet).\n", npmPackage)
		return nil
	}

	fmt.Printf("\nUpdating: npm install -g %s@latest\n", npmPackage)
	out, err := exec.CommandContext(ctx, "npm", "install", "-g", npmPackage+"@latest").CombinedOutput()
	if err != nil {
		return fmt.Errorf("npm install -g %s: %w\n%s", npmPackage, err, string(out))
	}
	fmt.Print(string(out))
	fmt.Println("✓ updated — open a new terminal, or re-run `omniharness --version`")
	return nil
}
