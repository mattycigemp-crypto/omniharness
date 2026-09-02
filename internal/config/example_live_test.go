package config

import "testing"

// The shipped example is the file users copy; if it stops parsing, or a
// documented key stops binding, they find out and we do not.
func TestExampleConfigParses(t *testing.T) {
	cfg, err := Load("../../config/omniharness.example.toml")
	if err != nil {
		t.Fatalf("example config must parse: %v", err)
	}
	if cfg.Policy.ShellAllowed {
		t.Error("the example must ship with the shell off")
	}
}
