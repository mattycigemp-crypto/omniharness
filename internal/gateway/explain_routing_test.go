package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

func TestExplainRoutingDecodesRealShape(t *testing.T) {
	// The exact response shape of OmniRoute's GET /v1/explain/routing handler
	// (open-sse/services/routing: RoutingEvent, ProviderQuality,
	// classifyQuality), read from the real source rather than guessed at.
	_, c := fakeOmniRoute(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/explain/routing" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("limit"); got != "25" {
			t.Errorf("limit query param = %q, want 25", got)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"object":      "routing_explain",
			"sinks":       []string{"memory"},
			"otelEnabled": false,
			"events": []map[string]any{{
				"requestId": "req_1", "provider": "cursor", "model": "claude-opus-4-8",
				"strategy": "auto", "latencyMs": 812, "ttftMs": 140, "itlMs": nil,
				"inputTokens": 1200, "outputTokens": 340, "cost": 0.014,
				"retries": 0, "fallbackUsed": false, "outcome": "success",
				"status": 200, "finishReason": "stop", "connectionId": nil, "ts": 1735000000000,
			}},
			"quality": []map[string]any{{
				"provider": "cursor", "model": "claude-opus-4-8",
				"operational": 0.94, "semantic": nil, "confidence": 0.8,
				"semanticConfidence": nil, "samples": 40, "anomalies": 1, "rateLimited": 0,
				"successEwma": 0.96, "latencyEwmaMs": 790.0, "ttftEwmaMs": 130.0,
				"recencyMs": 4200, "lastTs": 1735000000000,
				"classification": "healthy",
			}},
			"otel": map[string]any{},
		})
	})

	got, err := c.ExplainRouting(context.Background(), 25)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("got nil RoutingExplain for a 200 response")
	}
	if len(got.Events) != 1 {
		t.Fatalf("events = %+v", got.Events)
	}
	ev := got.Events[0]
	if ev.RequestID != "req_1" || ev.Provider != "cursor" || ev.Model != "claude-opus-4-8" {
		t.Errorf("event identity = %+v", ev)
	}
	if ev.TTFTMS == nil || *ev.TTFTMS != 140 {
		t.Errorf("ttftMs = %v, want 140", ev.TTFTMS)
	}
	if ev.ITLMS != nil {
		t.Errorf("itlMs = %v, want nil (non-streaming)", ev.ITLMS)
	}
	if ev.Cost == nil || *ev.Cost != 0.014 {
		t.Errorf("cost = %v, want 0.014", ev.Cost)
	}
	if ev.FinishReason == nil || *ev.FinishReason != "stop" {
		t.Errorf("finishReason = %v, want stop", ev.FinishReason)
	}

	if len(got.Quality) != 1 {
		t.Fatalf("quality = %+v", got.Quality)
	}
	q := got.Quality[0]
	if q.Classification != QualityHealthy {
		t.Errorf("classification = %q, want healthy", q.Classification)
	}
	if q.Samples != 40 || q.Confidence != 0.8 {
		t.Errorf("quality sample fields = %+v", q)
	}
}

func TestExplainRoutingNoLimitOmitsQueryParam(t *testing.T) {
	_, c := fakeOmniRoute(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" {
			t.Errorf("query = %q, want none when limit is 0", r.URL.RawQuery)
		}
		json.NewEncoder(w).Encode(map[string]any{"events": []any{}, "quality": []any{}})
	})
	if _, err := c.ExplainRouting(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
}

// The endpoint is recent — a harness talking to an older OmniRoute instance
// must not treat "this gateway predates the endpoint" as a failure. doctor's
// job is to report what it found, not to error out because one optional
// diagnostic isn't available.
func TestExplainRouting404IsNotAnError(t *testing.T) {
	_, c := fakeOmniRoute(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	got, err := c.ExplainRouting(context.Background(), 10)
	if err != nil {
		t.Fatalf("404 must not be an error, got %v", err)
	}
	if got != nil {
		t.Errorf("got = %+v, want nil for an unsupported gateway", got)
	}
}

func TestExplainRoutingAuthFailureIsAnError(t *testing.T) {
	// A 401 is a real failure — distinct from "endpoint doesn't exist" — and
	// must still surface as one, or a misconfigured key looks identical to an
	// old gateway.
	_, c := fakeOmniRoute(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":"Authentication required"}`))
	})
	_, err := c.ExplainRouting(context.Background(), 10)
	if err == nil {
		t.Fatal("a 401 must be reported as an error")
	}
	var gwErr *Error
	if ok := asGatewayError(err, &gwErr); !ok || gwErr.Kind != KindAuth {
		t.Errorf("err = %v, want KindAuth", err)
	}
}

func asGatewayError(err error, target **Error) bool {
	e, ok := err.(*Error)
	if ok {
		*target = e
	}
	return ok
}
