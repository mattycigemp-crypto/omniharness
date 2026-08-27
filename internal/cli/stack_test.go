package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"omniharness/internal/config"
	"omniharness/internal/testutil"
)

// tempConfigPath returns a fresh temp config path and restores rootOpts
// afterwards. NOTE: set rootOpts.ConfigPath AFTER NewRootCmd() — the flag
// binding resets the var to its default at construction time.
func tempConfigPath(t *testing.T) string {
	t.Helper()
	old := rootOpts.ConfigPath
	path := filepath.Join(t.TempDir(), "cfg.toml")
	t.Cleanup(func() { rootOpts.ConfigPath = old })
	return path
}

// unreachableEndpoint points OMNIROUTE_URL at a port nothing listens on so
// the catalog fetch fails instantly (connection refused) and the built-in
// combo fallback is used — tests must never touch the developer's live server.
func unreachableEndpoint(t *testing.T) {
	t.Helper()
	t.Setenv("OMNIROUTE_URL", "http://127.0.0.1:9")
}

func TestStackListShowsCurrentCombo(t *testing.T) {
	unreachableEndpoint(t)
	path := tempConfigPath(t)
	if err := config.WriteDefault(path); err != nil {
		t.Fatal(err)
	}

	root := NewRootCmd()
	rootOpts.ConfigPath = path
	root.SetArgs([]string{"stack"})
	out := captureStdout(t, func() {
		if err := root.Execute(); err != nil {
			t.Fatalf("stack: %v", err)
		}
	})
	if !strings.Contains(out, "auto/best-coding") {
		t.Errorf("stack list missing default combo:\n%s", out)
	}
	if !strings.Contains(out, "current combo:") {
		t.Errorf("stack list missing current-combo line:\n%s", out)
	}
}

func TestStackSetPersistsAndShows(t *testing.T) {
	unreachableEndpoint(t)
	path := tempConfigPath(t)
	if err := config.WriteDefault(path); err != nil {
		t.Fatal(err)
	}

	root := NewRootCmd()
	rootOpts.ConfigPath = path
	root.SetArgs([]string{"stack", "set", "auto/best-reasoning"})
	out := captureStdout(t, func() {
		if err := root.Execute(); err != nil {
			t.Fatalf("stack set: %v", err)
		}
	})
	if !strings.Contains(out, `combo set to "auto/best-reasoning"`) {
		t.Errorf("set output:\n%s", out)
	}

	// Persisted on disk, reloadable, and reported by `stack show`.
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Models.Default != "auto/best-reasoning" {
		t.Fatalf("persisted combo = %q", cfg.Models.Default)
	}

	// NewRootCmd re-binds the flag (resetting ConfigPath), so re-point it.
	root2 := NewRootCmd()
	rootOpts.ConfigPath = path
	root2.SetArgs([]string{"stack", "show"})
	out2 := captureStdout(t, func() {
		if err := root2.Execute(); err != nil {
			t.Fatalf("stack show: %v", err)
		}
	})
	if !strings.Contains(out2, "combo: auto/best-reasoning") {
		t.Errorf("show output:\n%s", out2)
	}
	if !strings.Contains(out2, "capability defaults") {
		t.Errorf("show should list capability defaults:\n%s", out2)
	}
}

func TestStackSetSpecificModel(t *testing.T) {
	unreachableEndpoint(t)
	path := tempConfigPath(t)
	if err := config.WriteDefault(path); err != nil {
		t.Fatal(err)
	}
	root := NewRootCmd()
	rootOpts.ConfigPath = path
	root.SetArgs([]string{"stack", "set", "openai/gpt-5.4"})
	if err := root.Execute(); err != nil {
		t.Fatalf("stack set specific model: %v", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Models.Default != "openai/gpt-5.4" {
		t.Fatalf("persisted combo = %q", cfg.Models.Default)
	}
}

func TestStackSetUnknownFails(t *testing.T) {
	unreachableEndpoint(t)
	path := tempConfigPath(t)
	if err := config.WriteDefault(path); err != nil {
		t.Fatal(err)
	}
	root := NewRootCmd()
	rootOpts.ConfigPath = path
	root.SetArgs([]string{"stack", "set", "not-a-model"})
	err := root.Execute()
	if err == nil {
		t.Fatal("expected error for malformed combo")
	}
	if !strings.Contains(err.Error(), "invalid model combo") {
		t.Fatalf("error = %v", err)
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Fatal("config file must be untouched on invalid combo")
	}
}

func TestStackSetRejectsUnknownCatalogIdWhenLive(t *testing.T) {
	path := tempConfigPath(t)
	if err := config.WriteDefault(path); err != nil {
		t.Fatal(err)
	}
	fake := testutil.NewFakeOmniRoute(t)
	fake.CatalogIDs = []string{"auto/best-coding", "openai/gpt-5.4"}
	t.Setenv("OMNIROUTE_URL", fake.URL())

	root := NewRootCmd()
	rootOpts.ConfigPath = path
	root.SetArgs([]string{"stack", "set", "anthropic/sonnet"})
	err := root.Execute()
	if err == nil {
		t.Fatal("expected error for id absent from the live catalog")
	}
	if !strings.Contains(err.Error(), "invalid model combo") {
		t.Fatalf("error = %v", err)
	}
}

func TestStackSetNeverWritesAPIKey(t *testing.T) {
	unreachableEndpoint(t)
	path := tempConfigPath(t)
	if err := config.WriteDefault(path); err != nil {
		t.Fatal(err)
	}
	// Simulate the key coming from the environment: it must never be written
	// back to disk by the save path.
	t.Setenv("OMNIROUTE_API_KEY", "sk-stack-leak-guard")

	root := NewRootCmd()
	rootOpts.ConfigPath = path
	root.SetArgs([]string{"stack", "set", "auto/best-fast"})
	if err := root.Execute(); err != nil {
		t.Fatalf("stack set: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// API key is now persisted for convenience.
	if !strings.Contains(string(data), "sk-stack-leak-guard") {
		t.Fatal("stack set should persist the API key to the config file")
	}
}
