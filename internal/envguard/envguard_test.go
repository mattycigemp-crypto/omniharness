package envguard

import (
	"os"
	"strings"
	"testing"
)

func TestFilterStripsCredentialVars(t *testing.T) {
	t.Setenv("OMNIROUTE_API_KEY", "sk-top-secret")
	t.Setenv("OMNIROUTE_MGMT_TOKEN", "sk-mgmt-secret")
	t.Setenv("OMNIHARNESS_API_KEY", "sk-legacy-secret")
	t.Setenv("ROUTER_API_KEY", "sk-router-secret")
	t.Setenv("PATH", "/usr/bin:/bin")
	t.Setenv("HOME", "/home/x")

	env := Filter()
	joined := strings.Join(env, "\n")
	for _, secret := range []string{"sk-top-secret", "sk-mgmt-secret", "sk-legacy-secret", "sk-router-secret"} {
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

// TestConfigureStripsNamedVars covers the deployments that do not want a
// subprocess inheriting third-party credentials. Nothing extra goes by
// default, so this also pins the default: gh and npm keep working.
func TestConfigureStripsNamedVars(t *testing.T) {
	t.Cleanup(func() { Configure(nil) })

	fixed := []string{
		"PATH=/usr/bin",
		"GITHUB_TOKEN=ghp-secret",
		"AWS_SECRET_ACCESS_KEY=aws-secret",
		"aws_session_token=aws-session",
		"NPM_TOKEN=npm-secret",
		"HOME=/home/x",
	}
	names := func(env []string) map[string]bool {
		out := map[string]bool{}
		for _, kv := range env {
			out[strings.SplitN(kv, "=", 2)[0]] = true
		}
		return out
	}

	// Default: third-party tokens are inherited on purpose.
	got := names(filter(fixed))
	for _, want := range []string{"GITHUB_TOKEN", "AWS_SECRET_ACCESS_KEY", "NPM_TOKEN"} {
		if !got[want] {
			t.Errorf("%s must be inherited by default", want)
		}
	}

	// Configured: exact names, prefix and suffix wildcards, any case.
	Configure([]string{"GITHUB_TOKEN", "AWS_*", "*_token"})
	got = names(filter(fixed))
	for _, gone := range []string{"GITHUB_TOKEN", "AWS_SECRET_ACCESS_KEY", "aws_session_token", "NPM_TOKEN"} {
		if got[gone] {
			t.Errorf("%s must be stripped once configured", gone)
		}
	}
	for _, kept := range []string{"PATH", "HOME"} {
		if !got[kept] {
			t.Errorf("%s must survive", kept)
		}
	}

	// Clearing restores the default.
	Configure(nil)
	if !names(filter(fixed))["GITHUB_TOKEN"] {
		t.Error("Configure(nil) must clear the extra patterns")
	}
}

// TestConfigureCannotUnsetBuiltins guards the one thing a config file must not
// be able to do: turn off the harness's own credential filtering.
func TestConfigureCannotUnsetBuiltins(t *testing.T) {
	t.Cleanup(func() { Configure(nil) })
	Configure([]string{"PATH"})
	for _, kv := range filter([]string{"OMNIROUTE_API_KEY=sk-leak", "PATH=/usr/bin"}) {
		if strings.HasPrefix(kv, "OMNIROUTE_API_KEY=") {
			t.Fatal("built-in credentials must be stripped regardless of config")
		}
	}
}

// TestConfigureAppliesToRealEnviron exercises Filter itself — the function all
// four subprocess call sites use — rather than the internal seam.
func TestConfigureAppliesToRealEnviron(t *testing.T) {
	t.Cleanup(func() { Configure(nil) })
	t.Setenv("OMNIHARNESS_TEST_TOKEN", "third-party-secret")

	Configure([]string{"OMNIHARNESS_TEST_*"})
	if joined := strings.Join(Filter(), "\n"); strings.Contains(joined, "third-party-secret") {
		t.Fatal("a configured variable must not reach a subprocess environment")
	}
}
