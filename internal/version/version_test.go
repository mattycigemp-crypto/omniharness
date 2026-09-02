package version

import (
	"regexp"
	"strings"
	"testing"
)

func TestShortOmitsTheProgramName(t *testing.T) {
	// cobra prints its own "<name> version <x>" around whatever it is given.
	// Short() feeding it the program name again produced
	// "omniharness version omniharness 0.2.1 (abc1234)". Keep it out.
	t.Cleanup(restore(Version, Commit))
	Version, Commit = "0.2.1", "abc1234"

	got := Short()
	if got != "0.2.1 (abc1234)" {
		t.Fatalf("Short() = %q, want %q", got, "0.2.1 (abc1234)")
	}
	if strings.Contains(got, "omniharness") {
		t.Error("Short() must not repeat the program name")
	}
}

func TestStringStandsOnItsOwn(t *testing.T) {
	t.Cleanup(restore(Version, Commit))
	Version, Commit = "0.2.1", "abc1234"

	if got := String(); got != "omniharness 0.2.1 (abc1234)" {
		t.Fatalf("String() = %q, want %q", got, "omniharness 0.2.1 (abc1234)")
	}
}

func TestDefaultsAreReplaceableAtBuildTime(t *testing.T) {
	// These are set with -ldflags -X, which only works on plain string vars
	// with no initialiser trickery. A non-empty default also means an
	// unstamped build says something rather than printing " ()".
	if Version == "" || Commit == "" {
		t.Fatal("the defaults must not be empty")
	}
	if !regexp.MustCompile(`^\d+\.\d+\.\d+$`).MatchString(Version) {
		t.Errorf("default Version = %q, want a semver triple", Version)
	}
	if got := Short(); !regexp.MustCompile(`^\S+ \(\S+\)$`).MatchString(got) {
		t.Errorf("Short() = %q, want \"<version> (<commit>)\"", got)
	}
}

func restore(version, commit string) func() {
	return func() { Version, Commit = version, commit }
}
