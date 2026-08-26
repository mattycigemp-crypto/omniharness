// Package gateway is the ONLY integration point between OmniHarness and
// OmniRoute. It speaks the OpenAI-compatible chat completions dialect that
// OmniRoute's gateway exposes, addressing models as "provider/model". OmniHarness
// never touches provider credentials, accounts or upstream routing — it
// expresses model-selection intent and lets OmniRoute execute.
package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Kind classifies gateway failures for the repair engine.
type Kind string

const (
	KindNetwork    Kind = "network"
	KindAuth       Kind = "auth"
	KindRateLimit  Kind = "rate_limit"
	KindServer     Kind = "server"
	KindBadRequest Kind = "bad_request"
	KindTimeout    Kind = "timeout"
	KindUnknown    Kind = "unknown"
)

// Error is a typed gateway error.
type Error struct {
	Kind    Kind
	Status  int
	Message string
}

func (e *Error) Error() string {
	return fmt.Sprintf("omniroute %s: %s (http %d)", e.Kind, e.Message, e.Status)
}

// Classify converts an HTTP status into a Kind.
func Classify(status int) Kind {
	switch {
	case status == 401 || status == 403:
		return KindAuth
	case status == 429:
		return KindRateLimit
	case status >= 500:
		return KindServer
	case status >= 400:
		return KindBadRequest
	}
	return KindUnknown
}

// Message is one chat message in OpenAI wire format.
type Message struct {
	Role       string     `json:"role"` // system | user | assistant | tool
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Name       string     `json:"name,omitempty"`
}

// ToolCall is a function invocation requested by the model.
type ToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"` // JSON-encoded arguments
	} `json:"function"`
}

// ToolSpec describes an available tool to the model.
type ToolSpec struct {
	Type     string `json:"type"`
	Function struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		Parameters  map[string]any `json:"parameters"` // JSON schema
	} `json:"function"`
}

// ChatRequest is an OpenAI-style chat completion request.
type ChatRequest struct {
	Model       string     `json:"model"`
	Messages    []Message  `json:"messages"`
	Tools       []ToolSpec `json:"tools,omitempty"`
	Temperature float64    `json:"temperature,omitempty"`
	MaxTokens   int        `json:"max_tokens,omitempty"`
	Stream      bool       `json:"stream"`
}

// ChatResponse is an OpenAI-style chat completion response.
type ChatResponse struct {
	Choices []struct {
		Index        int     `json:"index"`
		Message      Message `json:"message"`
		FinishReason string  `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int64 `json:"prompt_tokens"`
		CompletionTokens int64 `json:"completion_tokens"`
		TotalTokens      int64 `json:"total_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error,omitempty"`
}

// Client talks to an OmniRoute server.
type Client struct {
	endpoint string
	apiKey   string
	http     *http.Client
}

// Redact replaces every occurrence of secret in text with a placeholder. It is
// applied to every error message the client produces so a credential can never
// leak through server error bodies, proxy diagnostics or transport errors.
func Redact(secret, text string) string {
	if secret == "" || text == "" {
		return text
	}
	return strings.ReplaceAll(text, secret, "[REDACTED]")
}

// redact applies key redaction to arbitrary text.
func (c *Client) redact(text string) string { return Redact(c.apiKey, text) }

// errf builds a typed gateway error with the API key redacted from the message.
func (c *Client) errf(kind Kind, status int, format string, args ...any) error {
	return &Error{Kind: kind, Status: status, Message: c.redact(fmt.Sprintf(format, args...))}
}

// New creates a gateway client. Timeout bounds a single request.
func New(endpoint string, timeout time.Duration, apiKey string) *Client {
	return &Client{
		endpoint: strings.TrimSuffix(endpoint, "/"),
		apiKey:   apiKey,
		http:     &http.Client{Timeout: timeout},
	}
}

// NewWithHTTP builds a client with a custom HTTP transport (used in tests).
func NewWithHTTP(endpoint string, timeout time.Duration, apiKey string, hc *http.Client) *Client {
	c := New(endpoint, timeout, apiKey)
	c.http = hc
	return c
}

// SetAPIKey swaps the credential used on subsequent requests. Intended for
// interactive startup (the user pastes a key into the harness before any agent
// runs); call it before starting concurrent work. The key is held in memory
// only and is never persisted or logged.
func (c *Client) SetAPIKey(key string) { c.apiKey = key }

// Chat performs a chat completion request.
func (c *Client) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, c.errf(KindBadRequest, 0, "encode request: %v", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, c.errf(KindNetwork, 0, "build request: %v", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	resp, err := c.http.Do(httpReq)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, c.errf(KindTimeout, 0, "request timed out")
		}
		return nil, c.errf(KindNetwork, 0, "%v", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, c.errf(KindNetwork, 0, "read response: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		msg := strings.TrimSpace(string(raw))
		if len(msg) > 500 {
			msg = msg[:500]
		}
		return nil, c.errf(Classify(resp.StatusCode), resp.StatusCode, "%s", msg)
	}
	var out ChatResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, c.errf(KindBadRequest, 0, "decode response: %v", err)
	}
	if out.Error != nil {
		return nil, c.errf(KindServer, resp.StatusCode, "%s", out.Error.Message)
	}
	return &out, nil
}

// Ping verifies the OmniRoute server is reachable. It probes a few well-known
// health-ish paths; any HTTP response (even 404/401) proves the server is up.
func (c *Client) Ping(ctx context.Context) error {
	paths := []string{"/healthz", "/api/health", "/api/providers", "/v1/models"}
	var last error
	for _, p := range paths {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint+p, nil)
		if err != nil {
			return err
		}
		resp, err := c.http.Do(req)
		if err == nil {
			resp.Body.Close()
			return nil // server answered
		}
		last = err
	}
	return c.errf(KindNetwork, 0, "server unreachable: %v", last)
}

// AuthState classifies the OmniRoute connectivity and authentication state.
// These mirror the states `omniharness doctor` reports.
type AuthState string

const (
	// AuthOK: reachable and the configured API key was accepted.
	AuthOK AuthState = "ok"
	// AuthNotRequired: reachable, but the server accepts anonymous requests
	// (OmniRoute's REQUIRE_API_KEY is off); no key is configured.
	AuthNotRequired AuthState = "auth_not_required"
	// AuthNotConfigured: reachable, the server requires an API key, and none
	// is configured.
	AuthNotConfigured AuthState = "auth_not_configured"
	// AuthRejected: reachable, a key is configured, and the server rejected it.
	AuthRejected AuthState = "auth_rejected"
	// AuthUnreachable: no HTTP response; the server is down or the endpoint is
	// wrong.
	AuthUnreachable AuthState = "unreachable"
	// AuthMisconfigured: reachable but the probe path misbehaved (e.g. an
	// unrelated server or a gateway that returns an unexpected status).
	AuthMisconfigured AuthState = "misconfigured"
)

// Diagnosis is the result of a connectivity/auth probe.
type Diagnosis struct {
	State    AuthState `json:"state"`
	Endpoint string    `json:"endpoint"`
	Status   int       `json:"status,omitempty"`
	Detail   string    `json:"detail,omitempty"`
}

// Diagnose probes the OmniRoute server and classifies reachability and
// authentication. The probe is GET /v1/models with the configured key sent as
// `Authorization: Bearer <key>` — the header OmniRoute's OpenAI-compatible
// gateway documents and validates (src/sse/services/auth.ts). Never returns an
// error; the classification lives in Diagnosis.State.
func (c *Client) Diagnose(ctx context.Context) Diagnosis {
	d := Diagnosis{Endpoint: c.endpoint}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint+"/v1/models", nil)
	if err != nil {
		d.State = AuthMisconfigured
		d.Detail = c.redact(err.Error())
		return d
	}
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		d.State = AuthUnreachable
		d.Detail = c.redact(err.Error())
		return d
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	d.Status = resp.StatusCode
	switch {
	case resp.StatusCode == http.StatusOK:
		if c.apiKey == "" {
			d.State = AuthNotRequired
			d.Detail = "server accepts anonymous requests; no API key configured"
		} else {
			d.State = AuthOK
			d.Detail = "reachable and authenticated"
		}
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		if c.apiKey == "" {
			d.State = AuthNotConfigured
			d.Detail = "server requires an API key; set OMNIROUTE_API_KEY"
		} else {
			d.State = AuthRejected
			d.Detail = "API key rejected by server"
		}
	case resp.StatusCode == 426: // Upgrade Required
		// OmniRoute listens with a WebSocket-only front door (returns 426
		// "Use WebSocket" to plain HTTP) alongside the HTTP API port. This is
		// almost always a wrong port, not a broken server.
		d.State = AuthMisconfigured
		d.Detail = "endpoint answered 426 Upgrade Required (WebSocket-only listener); point OMNIROUTE_URL at the HTTP API port (default http://127.0.0.1:20128)"
	default:
		d.State = AuthMisconfigured
		d.Detail = fmt.Sprintf("reachable; /v1/models returned HTTP %d", resp.StatusCode)
	}
	return d
}

// Provider describes one OmniRoute provider.
type Provider struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Channel string `json:"channel,omitempty"`
	Status  string `json:"status,omitempty"`
}

// ModelInfo describes a model in OmniRoute's catalog.
type ModelInfo struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Provider string `json:"provider,omitempty"`
}

// ListProviders fetches the provider catalog.
func (c *Client) ListProviders(ctx context.Context) ([]Provider, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint+"/api/providers", nil)
	if err != nil {
		return nil, c.errf(KindNetwork, 0, "build request: %v", err)
	}
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, c.errf(KindNetwork, 0, "%v", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, c.errf(KindNetwork, 0, "%v", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, c.errf(Classify(resp.StatusCode), resp.StatusCode, "list providers")
	}
	// OmniRoute returns {"connections":[{id, provider, authType, isActive,
	// testStatus, providerSpecificData…}]}. We decode only the fields we
	// display; anything else (including provider credentials embedded in
	// providerSpecificData) is dropped.
	var out struct {
		Connections []struct {
			ID         string `json:"id"`
			Name       string `json:"provider"`
			AuthType   string `json:"authType"`
			IsActive   bool   `json:"isActive"`
			TestStatus string `json:"testStatus"`
		} `json:"connections"`
	}
	if err := json.Unmarshal(raw, &out); err == nil && len(out.Connections) > 0 {
		providers := make([]Provider, 0, len(out.Connections))
		for _, c := range out.Connections {
			status := "inactive"
			if c.IsActive {
				status = "active"
			}
			if c.TestStatus != "" {
				status = c.TestStatus
			}
			providers = append(providers, Provider{
				ID:      c.ID,
				Name:    c.Name,
				Channel: c.AuthType,
				Status:  status,
			})
		}
		return providers, nil
	}
	// Some deployments return a bare array.
	var arr []Provider
	if err := json.Unmarshal(raw, &arr); err == nil {
		return arr, nil
	}
	return nil, c.errf(KindBadRequest, 0, "decode providers: %v", err)
}

// ListModels fetches the model catalog for a provider.
func (c *Client) ListModels(ctx context.Context, providerID string) ([]ModelInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint+"/api/providers/"+providerID+"/models", nil)
	if err != nil {
		return nil, c.errf(KindNetwork, 0, "build request: %v", err)
	}
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, c.errf(KindNetwork, 0, "%v", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, c.errf(KindNetwork, 0, "%v", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, c.errf(Classify(resp.StatusCode), resp.StatusCode, "list models")
	}
	var out struct {
		Models []ModelInfo `json:"models"`
	}
	if err := json.Unmarshal(raw, &out); err == nil && len(out.Models) > 0 {
		return out.Models, nil
	}
	// Some deployments return a bare array.
	var arr []ModelInfo
	if err := json.Unmarshal(raw, &arr); err == nil {
		return arr, nil
	}
	return nil, c.errf(KindBadRequest, 0, "decode models: %v", err)
}

// ListCatalog fetches the global model catalog (ids only). It mirrors the
// OpenAI-style /v1/models listing OmniRoute exposes; only the id fields are
// decoded — the payload can carry large per-model metadata we don't need.
// Returns ids in catalog order. When the catalog is unreachable or unreadable
// the error is classified (network vs auth vs availability).
func (c *Client) ListCatalog(ctx context.Context) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint+"/v1/models", nil)
	if err != nil {
		return nil, c.errf(KindNetwork, 0, "build request: %v", err)
	}
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, c.errf(KindNetwork, 0, "%v", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 12<<20))
	if err != nil {
		return nil, c.errf(KindNetwork, 0, "%v", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, c.errf(Classify(resp.StatusCode), resp.StatusCode, "list catalog")
	}
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, c.errf(KindBadRequest, 0, "decode catalog: %v", err)
	}
	ids := make([]string, 0, len(out.Data))
	for _, m := range out.Data {
		if m.ID != "" {
			ids = append(ids, m.ID)
		}
	}
	return ids, nil
}

// SplitModel splits a "provider/model" reference into its parts.
func SplitModel(ref string) (provider, model string) {
	i := strings.Index(ref, "/")
	if i < 0 {
		return "", ref
	}
	return ref[:i], ref[i+1:]
}
