package cli

import (
	"context"
	"strings"
	"testing"
	"time"

	"omniharness/internal/config"
	"omniharness/internal/gateway"
	"omniharness/internal/runtime"
	"omniharness/internal/testutil"
)

func authTestRuntime(t *testing.T, fake *testutil.FakeOmniRoute) *runtime.Runtime {
	t.Helper()
	cfg := config.Default()
	cfg.Persistence.Dir = t.TempDir()
	rt, err := runtime.New(cfg, runtime.Options{Gateway: fake.Client()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(rt.Close)
	return rt
}

func TestEnsureAuthNonInteractiveNoKeyErrors(t *testing.T) {
	fake := testutil.NewFakeOmniRoute(t)
	fake.RequireAPIKey = "sk-real"
	rt := authTestRuntime(t, fake)
	cfg := config.Default()

	err := ensureAuth(context.Background(), rt, cfg, authPrompter{interactive: false})
	if err == nil {
		t.Fatal("expected an error when no key is set and stdin is not a terminal")
	}
	if !strings.Contains(err.Error(), "OMNIROUTE_API_KEY") {
		t.Fatalf("error should point at OMNIROUTE_API_KEY, got: %v", err)
	}
}

func TestEnsureAuthUnreachableNonInteractiveErrors(t *testing.T) {
	// Point the runtime at a dead server: the error must still name the fix.
	cfg := config.Default()
	cfg.Persistence.Dir = t.TempDir()
	cfg.OmniRoute.Endpoint = "http://127.0.0.1:1"
	rt, err := runtime.New(cfg, runtime.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(rt.Close)

	err = ensureAuth(context.Background(), rt, cfg, authPrompter{interactive: false})
	if err == nil {
		t.Fatal("expected an error for an unreachable server with no key")
	}
	if !strings.Contains(err.Error(), "OMNIROUTE_API_KEY") {
		t.Fatalf("error should name the env var fix, got: %v", err)
	}
}

func TestEnsureAuthAnonymousModeNeedsNoKey(t *testing.T) {
	fake := testutil.NewFakeOmniRoute(t) // no enforcement → AuthNotRequired
	rt := authTestRuntime(t, fake)

	if err := ensureAuth(context.Background(), rt, config.Default(), authPrompter{interactive: false}); err != nil {
		t.Fatalf("anonymous mode should not require a key: %v", err)
	}
}

func TestEnsureAuthAlreadyConfiguredSkipsPrompt(t *testing.T) {
	fake := testutil.NewFakeOmniRoute(t)
	fake.RequireAPIKey = "sk-configured"
	cfg := config.Default()
	cfg.OmniRoute.APIKey = "sk-configured"
	rt := authTestRuntime(t, fake)

	if err := ensureAuth(context.Background(), rt, cfg, authPrompter{interactive: true, stdin: strings.NewReader("sk-pasted\n")}); err != nil {
		t.Fatalf("configured key should skip the prompt: %v", err)
	}
}

func TestEnsureAuthInteractivePromptsAndSetsKey(t *testing.T) {
	const pasted = "sk-pasted-42"
	fake := testutil.NewFakeOmniRoute(t)
	fake.RequireAPIKey = pasted
	rt := authTestRuntime(t, fake)

	err := ensureAuth(context.Background(), rt, config.Default(),
		authPrompter{interactive: true, stdin: strings.NewReader(pasted + "\n")})
	if err != nil {
		t.Fatalf("interactive paste should succeed: %v", err)
	}

	// The pasted key must actually authenticate now.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if d := rt.Gateway.Diagnose(ctx); d.State != gateway.AuthOK {
		t.Fatalf("after paste, diagnose = %s (%s)", d.State, d.Detail)
	}
}

func TestEnsureAuthInteractiveRejectedKeyWarnsButContinues(t *testing.T) {
	fake := testutil.NewFakeOmniRoute(t)
	fake.RequireAPIKey = "sk-real-key"
	rt := authTestRuntime(t, fake)

	err := ensureAuth(context.Background(), rt, config.Default(),
		authPrompter{interactive: true, stdin: strings.NewReader("sk-wrong-key\n")})
	if err != nil {
		t.Fatalf("a rejected key should warn, not abort the launch: %v", err)
	}
}

func TestPromptAPIKeyTrimsWhitespace(t *testing.T) {
	got, err := promptAPIKey(strings.NewReader("  sk-x-1234  \n"))
	if err != nil {
		t.Fatal(err)
	}
	if got != "sk-x-1234" {
		t.Fatalf("promptAPIKey = %q", got)
	}
}
