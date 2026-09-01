// Package version holds build metadata.
package version

// Version is the semantic version. Overridden at build time via
// -ldflags "-X omniharness/internal/version.Version=...".
var Version = "0.1.0"

// Commit is the git commit, when built from a checkout.
var Commit = "dev"

// Short renders the version without the program name: "0.2.1 (abc1234)".
// cobra prints its own "<name> version <Version>", so handing it String()
// produced "omniharness version omniharness 0.2.1 (abc1234)".
func Short() string {
	return Version + " (" + Commit + ")"
}

// String renders the full version string, for output that stands on its own.
func String() string {
	return "omniharness " + Short()
}
