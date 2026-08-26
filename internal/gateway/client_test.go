package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// fakeOmniRoute implements the minimal OmniRoute surface.
func fakeOmniRoute(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *Client) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv, New(srv.URL, 5*time.Second, "")
}

func TestChatSuccess(t *testing.T) {
	_, c := fakeOmniRoute(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("path = %s", r.URL.Path)
		}
		var req ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		if req.Model != "cursor/claude-x" {
			t.Errorf("model = %q", req.Model)
		}
		if len(req.Messages) != 2 || req.Messages[0].Role != "system" {
			t.Errorf("messages = %+v", req.Messages)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"index": 0,
				"message": map[string]any{
					"role":    "assistant",
					"content": "hello",
				},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15},
		})
	})
	resp, err := c.Chat(context.Background(), ChatRequest{
		Model: "cursor/claude-x",
		Messages: []Message{
			{Role: "system", Content: "be helpful"},
			{Role: "user", Content: "hi"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Choices[0].Message.Content != "hello" {
		t.Fatalf("content = %q", resp.Choices[0].Message.Content)
	}
	if resp.Usage.TotalTokens != 15 {
		t.Fatalf("usage = %+v", resp.Usage)
	}
}

func TestChatErrorClassification(t *testing.T) {
	_, c := fakeOmniRoute(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte("rate limited"))
	})
	_, err := c.Chat(context.Background(), ChatRequest{Model: "m", Messages: []Message{{Role: "user", Content: "x"}}})
	if err == nil {
		t.Fatal("expected error")
	}
	gwErr, ok := err.(*Error)
	if !ok {
		t.Fatalf("error type %T", err)
	}
	if gwErr.Kind != KindRateLimit {
		t.Fatalf("kind = %s", gwErr.Kind)
	}
}

func TestChatAuthError(t *testing.T) {
	_, c := fakeOmniRoute(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":{"message":"bad key"}}`))
	})
	_, err := c.Chat(context.Background(), ChatRequest{Model: "m"})
	if err == nil {
		t.Fatal("expected error")
	}
	if e, ok := err.(*Error); !ok || e.Kind != KindAuth {
		t.Fatalf("got %v", err)
	}
}

func TestChatNetworkError(t *testing.T) {
	c := New("http://127.0.0.1:1", time.Second, "") // nothing listens on port 1
	_, err := c.Chat(context.Background(), ChatRequest{Model: "m"})
	if err == nil {
		t.Fatal("expected error")
	}
	if e, ok := err.(*Error); !ok || e.Kind != KindNetwork {
		t.Fatalf("got %v", err)
	}
}

func TestChatToolCallRoundTrip(t *testing.T) {
	var got ChatRequest
	_, c := fakeOmniRoute(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&got)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"index": 0,
				"message": map[string]any{
					"role": "assistant",
					"tool_calls": []map[string]any{{
						"id":   "call_1",
						"type": "function",
						"function": map[string]any{
							"name":      "read_file",
							"arguments": `{"path":"a.txt"}`,
						},
					}},
				},
				"finish_reason": "tool_calls",
			}},
			"usage": map[string]any{},
		})
	})
	resp, err := c.Chat(context.Background(), ChatRequest{
		Model: "cursor/claude-x",
		Messages: []Message{
			{Role: "user", Content: "read a.txt"},
		},
		Tools: []ToolSpec{{Type: "function"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	tc := resp.Choices[0].Message.ToolCalls[0]
	if tc.ID != "call_1" || tc.Function.Name != "read_file" || tc.Function.Arguments != `{"path":"a.txt"}` {
		t.Fatalf("tool call %+v", tc)
	}
	if got.Model != "cursor/claude-x" {
		t.Fatalf("model = %q", got.Model)
	}
}

func TestPing(t *testing.T) {
	_, c := fakeOmniRoute(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	if err := c.Ping(context.Background()); err != nil {
		t.Fatalf("ping to answering server failed: %v", err)
	}
}

func TestPingUnreachable(t *testing.T) {
	c := New("http://127.0.0.1:1", 500*time.Millisecond, "")
	if err := c.Ping(context.Background()); err == nil {
		t.Fatal("expected ping failure")
	}
}

func TestListProvidersAndModels(t *testing.T) {
	_, c := fakeOmniRoute(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/providers":
			json.NewEncoder(w).Encode(map[string]any{
				"connections": []map[string]any{{"id": "p1", "provider": "cursor", "authType": "key", "isActive": true, "testStatus": "active"}},
			})
		case "/api/providers/p1/models":
			json.NewEncoder(w).Encode(map[string]any{
				"models": []map[string]any{{"id": "m1", "name": "claude-x"}},
			})
		default:
			w.WriteHeader(404)
		}
	})
	providers, err := c.ListProviders(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(providers) != 1 || providers[0].ID != "p1" {
		t.Fatalf("providers %+v", providers)
	}
	models, err := c.ListModels(context.Background(), "p1")
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].ID != "m1" {
		t.Fatalf("models %+v", models)
	}
}

func TestSplitModel(t *testing.T) {
	p, m := SplitModel("cursor/claude-x")
	if p != "cursor" || m != "claude-x" {
		t.Fatalf("%q %q", p, m)
	}
}
