// Package tools defines the tool system: a registry of native tools, MCP
// tools and plugin tools, each exposing structured metadata (name, description,
// JSON input schema, risk level, execution characteristics). All tool
// execution flows through the policy engine before it runs.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Risk classifies the danger of a tool invocation.
type Risk string

const (
	RiskLow      Risk = "low"
	RiskMedium   Risk = "medium"
	RiskHigh     Risk = "high"
	RiskCritical Risk = "critical"
)

// AllRisks lists risk classes in ascending severity.
func AllRisks() []Risk { return []Risk{RiskLow, RiskMedium, RiskHigh, RiskCritical} }

// Spec is the structured metadata of a tool.
type Spec struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"` // JSON schema
	Risk        Risk           `json:"risk"`
	// MutatesFS reports whether the tool can change files on disk.
	MutatesFS bool `json:"mutatesFs,omitempty"`
	// ExecutesCode reports whether the tool runs arbitrary commands/code.
	ExecutesCode bool `json:"executesCode,omitempty"`
	// Network reports whether the tool can reach the network.
	Network bool `json:"network,omitempty"`
}

// Result of a tool invocation.
type Result struct {
	Output string `json:"output"`
	// Artifact marks outputs worth persisting (files produced, etc.).
	Artifact bool `json:"artifact,omitempty"`
	// Replan marks that this call is a request to restructure the task's
	// execution — the agent that ran it has decided the current plan is too
	// small for what it has actually found. The caller (agent.Agent) records
	// the reason (Output); the orchestrator acts on it once the current step
	// finishes.
	Replan bool `json:"replan,omitempty"`
}

// Tool is the execution interface.
type Tool interface {
	Spec() Spec
	Run(ctx context.Context, input map[string]any) (Result, error)
}

// Registry holds every available tool.
type Registry struct {
	mu    sync.RWMutex
	tools map[string]Tool
}

// NewRegistry creates an empty registry.
func NewRegistry() *Registry {
	return &Registry{tools: make(map[string]Tool)}
}

// Register adds a tool. Duplicate names are an error.
func (r *Registry) Register(t Tool) error {
	s := t.Spec()
	if s.Name == "" {
		return fmt.Errorf("tool with empty name")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.tools[s.Name]; exists {
		return fmt.Errorf("tool %q already registered", s.Name)
	}
	r.tools[s.Name] = t
	return nil
}

// Get returns a tool by name.
func (r *Registry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	return t, ok
}

// List returns all tool specs, sorted by name.
func (r *Registry) List() []Spec {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Spec, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, t.Spec())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Names returns tool names only.
func (r *Registry) Names() []string {
	specs := r.List()
	out := make([]string, len(specs))
	for i, s := range specs {
		out[i] = s.Name
	}
	return out
}

// ToGatewaySpecs converts registry specs to gateway tool specs for the model.
func (r *Registry) ToGatewaySpecs() []GatewaySpec {
	specs := r.List()
	out := make([]GatewaySpec, 0, len(specs))
	for _, s := range specs {
		out = append(out, GatewaySpec{Name: s.Name, Description: s.Description, Parameters: s.Parameters})
	}
	return out
}

// GatewaySpec is the model-facing tool description.
type GatewaySpec struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

// DecodeArgs decodes a JSON-encoded argument string into a map.
func DecodeArgs(raw string) (map[string]any, error) {
	var m map[string]any
	if strings.TrimSpace(raw) == "" {
		return map[string]any{}, nil
	}
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("invalid tool arguments: %w", err)
	}
	return m, nil
}

// StringArg extracts a string argument.
func StringArg(input map[string]any, key string) (string, error) {
	v, ok := input[key]
	if !ok {
		return "", fmt.Errorf("missing required argument %q", key)
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("argument %q must be a string", key)
	}
	return s, nil
}

// BoolArg extracts an optional boolean argument.
func BoolArg(input map[string]any, key string, def bool) bool {
	v, ok := input[key]
	if !ok {
		return def
	}
	b, ok := v.(bool)
	if !ok {
		return def
	}
	return b
}
