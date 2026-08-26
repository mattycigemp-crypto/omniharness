package gateway_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"omniharness/internal/gateway"
	"omniharness/internal/testutil"
)

const testKey = "sk-test-0123456789abcdef"

func TestAuthenticatedRequestsCarryBearer(t *testing.T) {
	fake := testutil.NewFakeOmniRoute(t)
	fake.RequireAPIKey = testKey
	c := fake.ClientWithKey(testKey)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := c.Chat(ctx, gateway.ChatRequest{Model: "fake/m1", Messages: []gateway.Message{{Role: "user", Content: "hi"}}}); err != nil {
		t.Fatalf("chat: %v", err)
	}
	if _, err := c.ListProviders(ctx); err != nil {
		t.Fatalf("providers: %v", err)
	}
	if _, err := c.ListModels(ctx, "fake"); err != nil {
		t.Fatalf("models: %v", err)
	}
	if d := c.Diagnose(ctx); d.State != gateway.AuthOK {
		t.Fatalf("diagnose = %s (%s)", d.State, d.Detail)
	}

	for _, h := range fake.AuthHeaders {
		if h != "Bearer "+testKey {
			t.Fatalf("authorization header = %q, want Bearer %s", h, testKey)
		}
	}
}

func TestUnauthenticatedRequestsSendNoCredential(t *testing.T) {
	fake := testutil.NewFakeOmniRoute(t)
	c := fake.Client() // no key

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := c.Chat(ctx, gateway.ChatRequest{Model: "fake/m1"}); err != nil {
		t.Fatalf("chat: %v", err)
	}
	for _, h := range fake.AuthHeaders {
		if h != "" {
			t.Fatalf("authorization header = %q, want empty", h)
		}
	}
}

func TestAuthRejectionIsClassified(t *testing.T) {
	fake := testutil.NewFakeOmniRoute(t)
	fake.RequireAPIKey = testKey
	c := fake.ClientWithKey("sk-wrong-key")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := c.Chat(ctx, gateway.ChatRequest{Model: "fake/m1"})
	var gerr *gateway.Error
	if !errors.As(err, &gerr) {
		t.Fatalf("want *Error, got %T: %v", err, err)
	}
	if gerr.Kind != gateway.KindAuth {
		t.Fatalf("kind = %s, want auth", gerr.Kind)
	}
	if gerr.Status != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", gerr.Status)
	}
	if strings.Contains(gerr.Error(), "sk-wrong-key") {
		t.Fatalf("error leaks the credential: %s", gerr.Error())
	}
}

func TestErrorMessagesRedactServerEchoedCredentials(t *testing.T) {
	fake := testutil.NewFakeOmniRoute(t)
	fake.RequireAPIKey = testKey
	fake.BounceAuthHeader = true
	c := fake.ClientWithKey("sk-wrong-key")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// The fake echoes the Authorization header back inside the 401 body; the
	// client must redact it from the surfaced error.
	_, err := c.Chat(ctx, gateway.ChatRequest{Model: "fake/m1"})
	if err == nil {
		t.Fatal("expected auth failure")
	}
	if strings.Contains(err.Error(), testKey) {
		t.Fatalf("error leaks the credential: %s", err.Error())
	}
	if !strings.Contains(err.Error(), "[REDACTED]") {
		t.Fatalf("expected redaction placeholder in error: %s", err.Error())
	}
}

func TestDiagnoseDistinguishesStates(t *testing.T) {
	t.Run("authenticated", func(t *testing.T) {
		fake := testutil.NewFakeOmniRoute(t)
		fake.RequireAPIKey = testKey
		c := fake.ClientWithKey(testKey)
		d := c.Diagnose(context.Background())
		if d.State != gateway.AuthOK {
			t.Fatalf("state = %s (%s)", d.State, d.Detail)
		}
	})
	t.Run("auth not required", func(t *testing.T) {
		fake := testutil.NewFakeOmniRoute(t) // no enforcement
		c := fake.Client()
		d := c.Diagnose(context.Background())
		if d.State != gateway.AuthNotRequired {
			t.Fatalf("state = %s (%s)", d.State, d.Detail)
		}
	})
	t.Run("auth required, key missing", func(t *testing.T) {
		fake := testutil.NewFakeOmniRoute(t)
		fake.RequireAPIKey = testKey
		c := fake.Client()
		d := c.Diagnose(context.Background())
		if d.State != gateway.AuthNotConfigured {
			t.Fatalf("state = %s (%s)", d.State, d.Detail)
		}
	})
	t.Run("auth rejected", func(t *testing.T) {
		fake := testutil.NewFakeOmniRoute(t)
		fake.RequireAPIKey = testKey
		c := fake.ClientWithKey("sk-wrong")
		d := c.Diagnose(context.Background())
		if d.State != gateway.AuthRejected {
			t.Fatalf("state = %s (%s)", d.State, d.Detail)
		}
	})
	t.Run("unreachable", func(t *testing.T) {
		// A server that is immediately closed: connections are refused.
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		url := srv.URL
		srv.Close()
		c := gateway.New(url, time.Second, testKey)
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		d := c.Diagnose(ctx)
		if d.State != gateway.AuthUnreachable {
			t.Fatalf("state = %s (%s)", d.State, d.Detail)
		}
	})
	t.Run("misconfigured", func(t *testing.T) {
		// An HTTP server that answers every path with 500.
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "boom", 500)
		}))
		defer srv.Close()
		c := gateway.New(srv.URL, 3*time.Second, testKey)
		d := c.Diagnose(context.Background())
		if d.State != gateway.AuthMisconfigured {
			t.Fatalf("state = %s (%s)", d.State, d.Detail)
		}
	})
}

func TestDiagnoseNeverLeaksKey(t *testing.T) {
	fake := testutil.NewFakeOmniRoute(t)
	fake.RequireAPIKey = testKey
	fake.BounceAuthHeader = true
	c := fake.ClientWithKey(testKey)
	d := c.Diagnose(context.Background())
	if strings.Contains(d.Detail, testKey) {
		t.Fatalf("diagnosis leaks the key: %s", d.Detail)
	}
}

func TestSetAPIKeyAppliesToSubsequentRequests(t *testing.T) {
	fake := testutil.NewFakeOmniRoute(t)
	fake.RequireAPIKey = testKey
	c := fake.Client() // constructed without a key

	c.SetAPIKey(testKey)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := c.Chat(ctx, gateway.ChatRequest{Model: "fake/m1"}); err != nil {
		t.Fatalf("chat after SetAPIKey: %v", err)
	}
	if got := fake.AuthHeaders[len(fake.AuthHeaders)-1]; got != "Bearer "+testKey {
		t.Fatalf("authorization header = %q", got)
	}
}

func TestMalformedResponseFailsCleanly(t *testing.T) {
	fake := testutil.NewFakeOmniRoute(t, testutil.FakeStep{Malformed: true})
	c := fake.Client()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := c.Chat(ctx, gateway.ChatRequest{Model: "fake/m1", Messages: []gateway.Message{{Role: "user", Content: "hi"}}})
	if err == nil {
		t.Fatal("malformed response must produce an error")
	}
	var gerr *gateway.Error
	if !errors.As(err, &gerr) {
		t.Fatalf("want typed *Error, got %T", err)
	}
	if gerr.Kind != gateway.KindBadRequest {
		t.Fatalf("kind = %s, want bad_request", gerr.Kind)
	}
}

func TestRedact(t *testing.T) {
	if got := gateway.Redact("secret123", "auth secret123 failed"); got != "auth [REDACTED] failed" {
		t.Fatalf("Redact = %q", got)
	}
	if got := gateway.Redact("", "nothing to hide"); got != "nothing to hide" {
		t.Fatalf("empty secret must be a no-op, got %q", got)
	}
}
