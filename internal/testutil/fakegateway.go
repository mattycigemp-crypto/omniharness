// Package testutil provides a scripted fake OmniRoute gateway so the agent
// runtime, orchestrator, CLI and TUI can be tested without a live model.
package testutil

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"omniharness/internal/gateway"
)

// FakeStep is one scripted model response.
type FakeStep struct {
	// Content returned to the model caller.
	Content string
	// ToolCalls requested (finish_reason becomes tool_calls when non-empty).
	ToolCalls []gateway.ToolCall
	// StatusCode, when non-zero, makes the endpoint return that HTTP status.
	StatusCode int
	// Malformed, when set, returns a 200 with a garbage body (chaos testing:
	// the client must fail cleanly, never panic).
	Malformed bool
	// Delay slows the response (used to test concurrency).
	Delay time.Duration
}

// FakeOmniRoute is an in-process fake of OmniRoute's gateway.
type FakeOmniRoute struct {
	t *testing.T
	s *httptest.Server
	// steps is the script queue; each request pops the next step and the last
	// step repeats once the queue is exhausted.
	steps []FakeStep
	mu    sync.Mutex
	// Requests records every chat request for assertions.
	Requests []gateway.ChatRequest
	// FailChat, when set, makes every chat call return the error.
	FailChat error
	// ProviderFailure makes /api/providers return 502.
	ProviderFailure bool
	// RequireAPIKey, when set, mimics OmniRoute with REQUIRE_API_KEY enabled:
	// client endpoints return 401 AUTH_002 unless the Authorization header
	// carries exactly "Bearer <RequireAPIKey>".
	RequireAPIKey string
	// BounceAuthHeader, when set, echoes the request's Authorization header
	// back inside error bodies (tests redaction of server-echoed credentials).
	BounceAuthHeader bool
	// AuthHeaders records the Authorization header of every client request.
	AuthHeaders []string
	// UpgradeRequired, when set, makes /v1/models answer 426 "Use WebSocket."
	// exactly like OmniRoute's WebSocket-only listener.
	UpgradeRequired bool
	// CatalogIDs, when set, is returned by /v1/models instead of the default
	// single fake/m1 entry.
	CatalogIDs []string
	// RoutingQuality, when non-nil, is served from GET /v1/explain/routing —
	// each entry a "provider/model:classification" pair (e.g.
	// "cursor/opus:degraded"). Nil means the route is not registered at all,
	// which is how an OmniRoute instance that predates this endpoint behaves
	// (a plain 404, not an empty success) — the default, so most tests are
	// unaffected by this field existing.
	RoutingQuality []string
}

// NewFakeOmniRoute starts the fake server.
func NewFakeOmniRoute(t *testing.T, steps ...FakeStep) *FakeOmniRoute {
	t.Helper()
	f := &FakeOmniRoute{t: t, steps: steps}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		f.recordAuth(r)
		if f.UpgradeRequired {
			// Mirrors OmniRoute's WebSocket-only listener: plain HTTP gets 426.
			w.WriteHeader(426)
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"error":"upgrade_required","message":"Use WebSocket."}`)
			return
		}
		if !f.authOK(r) {
			f.writeAuthError(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		f.mu.Lock()
		catalog := f.CatalogIDs
		f.mu.Unlock()
		if len(catalog) == 0 {
			catalog = []string{"fake/m1"}
		}
		data := make([]map[string]any, 0, len(catalog))
		for _, id := range catalog {
			data = append(data, map[string]any{"id": id, "object": "model"})
		}
		json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data})
	})
	mux.HandleFunc("/v1/explain/routing", func(w http.ResponseWriter, r *http.Request) {
		// Every other optional field on this fake is read live, at request
		// time, so a test can set it any time before the request it affects —
		// see CatalogIDs below. This route was first registered only when the
		// field was already non-empty at construction, which meant setting
		// it right after NewFakeOmniRoute (the natural, and every other
		// field's, calling convention) silently did nothing.
		f.mu.Lock()
		configured := f.RoutingQuality
		f.mu.Unlock()
		if len(configured) == 0 {
			// No data configured reads the same as "this OmniRoute instance
			// predates the endpoint" — a plain 404, matching the real server.
			w.WriteHeader(http.StatusNotFound)
			return
		}
		f.recordAuth(r)
		if !f.authOK(r) {
			f.writeAuthError(w, r)
			return
		}
		quality := make([]map[string]any, 0, len(configured))
		for _, spec := range configured {
			parts := strings.SplitN(spec, ":", 2)
			providerModel, class := parts[0], "healthy"
			if len(parts) == 2 {
				class = parts[1]
			}
			pm := strings.SplitN(providerModel, "/", 2)
			provider, model := pm[0], ""
			if len(pm) == 2 {
				model = pm[1]
			}
			quality = append(quality, map[string]any{
				"provider": provider, "model": model, "classification": class,
				"operational": 0.8, "confidence": 0.9, "samples": 20,
			})
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"object": "routing_explain", "events": []any{}, "quality": quality,
		})
	})
	mux.HandleFunc("/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		f.recordAuth(r)
		if !f.authOK(r) {
			f.writeAuthError(w, r)
			return
		}
		var req gateway.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		f.mu.Lock()
		f.Requests = append(f.Requests, req)
		f.mu.Unlock()

		if f.FailChat != nil {
			http.Error(w, f.FailChat.Error(), 502)
			return
		}
		f.mu.Lock()
		var step FakeStep
		if len(f.steps) > 0 {
			step = f.steps[0]
			if len(f.steps) > 1 {
				f.steps = f.steps[1:]
			}
		}
		f.mu.Unlock()
		if step.StatusCode != 0 {
			http.Error(w, "scripted failure", step.StatusCode)
			return
		}
		if step.Malformed {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, "{this is not json at all")
			return
		}
		if step.Delay > 0 {
			time.Sleep(step.Delay)
		}
		w.Header().Set("Content-Type", "application/json")
		finish := "stop"
		if len(step.ToolCalls) > 0 {
			finish = "tool_calls"
		}
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"index":         0,
				"message":       map[string]any{"role": "assistant", "content": step.Content, "tool_calls": step.ToolCalls},
				"finish_reason": finish,
			}},
			"usage": map[string]any{
				"prompt_tokens":     100,
				"completion_tokens": 50,
				"total_tokens":      150,
			},
		})
	})
	mux.HandleFunc("/api/providers", func(w http.ResponseWriter, r *http.Request) {
		f.recordAuth(r)
		if !f.authOK(r) {
			f.writeAuthError(w, r)
			return
		}
		if f.ProviderFailure {
			http.Error(w, "unavailable", 502)
			return
		}
		// Mirrors the real OmniRoute shape: {"connections":[{id, provider,
		// authType, isActive, testStatus}]}.
		json.NewEncoder(w).Encode(map[string]any{
			"connections": []map[string]any{
				{"id": "conn-fake", "provider": "fake-provider", "authType": "key", "isActive": true, "testStatus": "active"},
			},
		})
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	mux.HandleFunc("/api/providers/", func(w http.ResponseWriter, r *http.Request) {
		f.recordAuth(r)
		if !f.authOK(r) {
			f.writeAuthError(w, r)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"models": []map[string]any{
				{"id": "m1", "name": "model-one", "provider": "fake"},
				{"id": "m2", "name": "model-two", "provider": "fake"},
			},
		})
	})
	f.s = httptest.NewServer(mux)
	t.Cleanup(f.s.Close)
	return f
}

// recordAuth captures the Authorization header of a client request.
func (f *FakeOmniRoute) recordAuth(r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.AuthHeaders = append(f.AuthHeaders, r.Header.Get("Authorization"))
}

// authOK reports whether the request passes the fake's auth gate.
func (f *FakeOmniRoute) authOK(r *http.Request) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.RequireAPIKey == "" {
		return true
	}
	return r.Header.Get("Authorization") == "Bearer "+f.RequireAPIKey
}

// writeAuthError responds 401 with the AUTH_002 shape OmniRoute uses, and —
// when BounceAuthHeader is set — echoes the credential back so tests can
// verify redaction.
func (f *FakeOmniRoute) writeAuthError(w http.ResponseWriter, r *http.Request) {
	body := `{"error":{"message":"Authentication required","type":"auth_error"}}`
	if f.BounceAuthHeader {
		body = `{"error":{"message":"Authorization: ` + r.Header.Get("Authorization") + `","type":"auth_error"}}`
	}
	http.Error(w, body, http.StatusUnauthorized)
}

// URL returns the fake server's base URL.
func (f *FakeOmniRoute) URL() string { return f.s.URL }

// Client returns a gateway client pointing at the fake, with no API key.
func (f *FakeOmniRoute) Client() *gateway.Client {
	return gateway.New(f.s.URL, 30*time.Second, "")
}

// ClientWithKey returns a gateway client pointing at the fake with an API key.
func (f *FakeOmniRoute) ClientWithKey(key string) *gateway.Client {
	return gateway.New(f.s.URL, 30*time.Second, key)
}

// RequestCount returns how many chat requests were received.
func (f *FakeOmniRoute) RequestCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.Requests)
}

// LastRequest returns the most recent chat request.
func (f *FakeOmniRoute) LastRequest() *gateway.ChatRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.Requests) == 0 {
		return nil
	}
	return &f.Requests[len(f.Requests)-1]
}

// RequestsSnapshot returns all chat requests.
func (f *FakeOmniRoute) RequestsSnapshot() []gateway.ChatRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]gateway.ChatRequest, len(f.Requests))
	copy(out, f.Requests)
	return out
}

// ToolCall builds a scripted tool call.
func ToolCall(id, name, args string) gateway.ToolCall {
	var tc gateway.ToolCall
	tc.ID = id
	tc.Type = "function"
	tc.Function.Name = name
	tc.Function.Arguments = args
	return tc
}
