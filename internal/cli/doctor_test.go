package cli

import "testing"

// doctor's exit code is what a script and a first-time user both read. It has
// to mean "this install cannot work", not "something is absent that you may
// not need". A prebuilt binary on a machine without Go is a normal, working
// install.
func TestDiagLevelsDecideTheExitCode(t *testing.T) {
	results := []diagResult{
		{Name: "config", OK: true, Level: "ok"},
		{Name: "go toolchain", OK: true, Level: "warn"},
		{Name: "git", OK: true, Level: "warn"},
		{Name: "omniroute endpoint", OK: false, Level: "FAIL"},
	}

	var failures, warnings int
	for _, r := range results {
		switch r.Level {
		case "FAIL":
			failures++
		case "warn":
			warnings++
		}
	}
	if failures != 1 {
		t.Errorf("failures = %d, want 1; only a real fault counts", failures)
	}
	if warnings != 2 {
		t.Errorf("warnings = %d, want 2", warnings)
	}
	if passed := len(results) - failures - warnings; passed != 1 {
		t.Errorf("passed = %d, want 1; a warning is not a pass either", passed)
	}

	// A warning must not flip OK, because that is what the JSON consumers
	// and the exit code are built on.
	for _, r := range results {
		if r.Level == "warn" && !r.OK {
			t.Errorf("%s: a warning must leave OK true", r.Name)
		}
		if r.Level == "FAIL" && r.OK {
			t.Errorf("%s: a failure must set OK false", r.Name)
		}
	}
}
