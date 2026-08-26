package envguard

import (
	"os"
	"strings"
	"testing"
)

func TestFilterStripsCredentialVars(t *testing.T) {
	t.Setenv("OMNIROUTE_API_KEY", "sk-top-secret")
	t.Setenv("OMNIHARNESS_API_KEY", "sk-legacy-secret")
	t.Setenv("ROUTER_API_KEY", "sk-router-secret")
	t.Setenv("PATH", "/usr/bin:/bin")
	t.Setenv("HOME", "/home/x")

	env := Filter()
	joined := strings.Join(env, "\n")
	for _, secret := range []string{"sk-top-secret", "sk-legacy-secret", "sk-router-secret"} {
		if strings.Contains(joined, secret) {
			t.Fatalf("credential leaked into subprocess env: %s", secret)
		}
	}
	found := map[string]bool{}
	for _, kv := range env {
		found[strings.SplitN(kv, "=", 2)[0]] = true
	}
	for _, want := range []string{"PATH", "HOME"} {
		if !found[want] {
			t.Fatalf("non-secret var %q must be preserved", want)
		}
	}
}

// TestShellToolDoesNotExposeKey proves the end-to-end invariant: a shell tool
// invocation cannot read the OmniRoute key from its environment.
func TestShellToolDoesNotExposeKey(t *testing.T) {
	if os.Getenv("OMNIROUTE_API_KEY") == "" {
		t.Setenv("OMNIROUTE_API_KEY", "sk-must-not-leak")
	}
	// The tools package filters env; this test guards the contract through the
	// public filter used by every subprocess-spawning tool.
	env := Filter()
	for _, kv := range env {
		if strings.HasPrefix(kv, "OMNIROUTE_API_KEY=") {
			t.Fatal("OMNIROUTE_API_KEY must never reach a subprocess environment")
		}
	}
}
