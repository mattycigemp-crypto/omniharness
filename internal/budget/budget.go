// Package budget defines resource boundaries for task execution and tracks
// consumption against them. Budgets are consulted by the strategy engine when
// choosing an execution plan and by the runtime to enforce limits.
package budget

import (
	"fmt"
	"sync"
	"time"
)

// Budget describes the resource boundaries of a task. Zero values mean
// "unlimited" for that dimension.
type Budget struct {
	MaxTokens     int64         `json:"maxTokens,omitempty"`       // total input+output tokens across all agents
	MaxCostUSD    float64       `json:"maxCostUsd,omitempty"`      // estimated cost ceiling
	MaxDuration   time.Duration `json:"maxDuration,omitempty"`     // wall-clock ceiling
	MaxAgents     int           `json:"maxAgents,omitempty"`       // concurrent agents
	MaxToolCalls  int           `json:"maxToolCalls,omitempty"`    // total tool calls
	MaxRepairCycl int           `json:"maxRepairCycles,omitempty"` // repair iterations per task
}

// Unlimited reports whether no budget at all was configured.
func (b Budget) Unlimited() bool {
	return b == Budget{}
}

// Usage tracks consumption against a Budget.
type Usage struct {
	Tokens       int64
	CostUSD      float64
	ToolCalls    int
	RepairCycles int
	StartedAt    time.Time
}

// Tracker accumulates usage and reports whether limits have been exceeded.
type Tracker struct {
	mu     sync.Mutex
	budget Budget
	usage  Usage
}

// NewTracker creates a tracker for the given budget.
func NewTracker(b Budget) *Tracker {
	return &Tracker{budget: b, usage: Usage{StartedAt: time.Now()}}
}

// AddTokens records token consumption and estimated cost.
func (t *Tracker) AddTokens(tokens int64, costUSD float64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.usage.Tokens += tokens
	t.usage.CostUSD += costUSD
}

// AddToolCall records one tool invocation.
func (t *Tracker) AddToolCall() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.usage.ToolCalls++
}

// AddRepairCycle records one repair iteration.
func (t *Tracker) AddRepairCycle() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.usage.RepairCycles++
}

// Usage returns a snapshot of current usage.
func (t *Tracker) Usage() Usage {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.usage
}

// Exceeded returns the first budget dimension that is exceeded, or "" if the
// task is still within budget.
func (t *Tracker) Exceeded() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	b, u := t.budget, t.usage
	if b.MaxTokens > 0 && u.Tokens >= b.MaxTokens {
		return fmt.Sprintf("token budget exceeded (%d >= %d)", u.Tokens, b.MaxTokens)
	}
	if b.MaxCostUSD > 0 && u.CostUSD >= b.MaxCostUSD {
		return fmt.Sprintf("cost budget exceeded ($%.4f >= $%.4f)", u.CostUSD, b.MaxCostUSD)
	}
	if b.MaxDuration > 0 && time.Since(u.StartedAt) >= b.MaxDuration {
		return fmt.Sprintf("duration budget exceeded (%s)", time.Since(u.StartedAt).Round(time.Second))
	}
	if b.MaxToolCalls > 0 && u.ToolCalls >= b.MaxToolCalls {
		return fmt.Sprintf("tool-call budget exceeded (%d >= %d)", u.ToolCalls, b.MaxToolCalls)
	}
	if b.MaxRepairCycl > 0 && u.RepairCycles >= b.MaxRepairCycl {
		return fmt.Sprintf("repair-cycle budget exceeded (%d >= %d)", u.RepairCycles, b.MaxRepairCycl)
	}
	return ""
}

// Budget returns the configured budget.
func (t *Tracker) Budget() Budget {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.budget
}
