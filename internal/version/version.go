// Package version holds build metadata.
package version

// Version is the semantic version. Overridden at build time via
// -ldflags "-X omniharness/internal/version.Version=...".
var Version = "0.1.0"

// Commit is the git commit, when built from a checkout.
var Commit = "dev"

// String renders the full version string.
func String() string {
	return "omniharness " + Version + " (" + Commit + ")"
}
