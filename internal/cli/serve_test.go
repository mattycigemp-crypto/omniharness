package cli

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestIsLoopbackHost(t *testing.T) {
	allowed := []string{
		"localhost", "localhost:20140", "LOCALHOST:20140",
		"127.0.0.1", "127.0.0.1:20140", "127.0.0.5:20140",
		"[::1]:20140", "::1",
	}
	for _, host := range allowed {
		if !isLoopbackHost(host) {
			t.Errorf("isLoopbackHost(%q) = false, want true", host)
		}
	}

	// A rebound request carries the attacker's hostname, because that is what
	// the browser resolved — this is the case the guard exists for.
	denied := []string{
		"", "evil.example.com", "evil.example.com:20140",
		"omniharness.localhost.evil.com", "10.0.0.5:20140", "example.com",
	}
	for _, host := range denied {
		if isLoopbackHost(host) {
			t.Errorf("isLoopbackHost(%q) = true, want false", host)
		}
	}
}

func TestGuardLoopback(t *testing.T) {
	reached := false
	guarded := guardLoopback(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))

	call := func(host, origin string) int {
		reached = false
		req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:20140/health", nil)
		req.Host = host
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		rec := httptest.NewRecorder()
		guarded.ServeHTTP(rec, req)
		return rec.Code
	}

	// A local client: no Origin, loopback Host.
	if code := call("127.0.0.1:20140", ""); code != http.StatusOK || !reached {
		t.Fatalf("loopback request: code=%d reached=%v, want 200 and reached", code, reached)
	}
	if code := call("localhost:20140", ""); code != http.StatusOK {
		t.Fatalf("localhost request: code=%d, want 200", code)
	}

	// DNS rebinding: the socket is loopback but the Host is the attacker's.
	if code := call("evil.example.com:20140", ""); code != http.StatusForbidden {
		t.Fatalf("rebound host: code=%d, want 403", code)
	}
	if reached {
		t.Fatal("a rebound request must not reach the handler")
	}

	// Any browser-issued cross-origin request, even to a loopback Host.
	if code := call("127.0.0.1:20140", "https://evil.example.com"); code != http.StatusForbidden {
		t.Fatalf("cross-origin: code=%d, want 403", code)
	}
	if reached {
		t.Fatal("a cross-origin request must not reach the handler")
	}
}
