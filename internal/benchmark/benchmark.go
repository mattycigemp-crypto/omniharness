// Package benchmark runs repeatable tasks across models/strategies and
// produces machine-readable results. Benchmarks never hardcode conclusions —
// they measure real execution through the normal runtime.
package benchmark

import (
	"context"
	"os"
	"strings"
	"time"

	"omniharness/internal/model"
	"omniharness/internal/runtime"
	"omniharness/internal/task"
)

// Case is one repeatable benchmark task.
type Case struct {
	ID     string
	Prompt string
	// Check evaluates the produced artifact/result. Returns pass and detail.
	Check func(tsk *task.Task) (bool, string)
	// WorkspacePrep prepares a scratch workspace for the case.
	WorkspacePrep func(dir string) error
}

// BuiltinCases returns the built-in benchmark tasks.
func BuiltinCases() []Case {
	return []Case{
		{
			ID:     "write-fib",
			Prompt: "Write a Go function fibonacci(n int) int that returns the n-th Fibonacci number, put it in a file called fib.go, and report the function. The function must handle n=0 returning 0.",
			Check: func(tsk *task.Task) (bool, string) {
				artifacts := strings.Join(tsk.Result.Artifacts, " ")
				if !strings.Contains(artifacts, "fib.go") {
					return false, "fib.go not among artifacts"
				}
				return true, "fib.go produced"
			},
		},
		{
			ID:     "explain-http",
			Prompt: "Explain, in a few sentences, the difference between HTTP/1.1 and HTTP/2. Include the term multiplexing.",
			Check: func(tsk *task.Task) (bool, string) {
				out := tsk.Result.Output
				lower := strings.ToLower(out)
				if !strings.Contains(lower, "http/2") || !strings.Contains(lower, "multiplex") {
					return false, "missing http/2 or multiplexing"
				}
				return true, "key terms present"
			},
		},
	}
}

// RunOptions configure a benchmark run.
type RunOptions struct {
	Models     []string // provider/model refs to run (empty = config default)
	Cases      []string // case IDs (empty = all)
	Iterations int
	Workspace  string // scratch dir for file-producing cases
}

// Result is one measured execution.
type Result struct {
	CaseID    string  `json:"caseId"`
	Model     string  `json:"model"`
	Strategy  string  `json:"strategy"`
	Iteration int     `json:"iteration"`
	Passed    bool    `json:"passed"`
	Detail    string  `json:"detail,omitempty"`
	LatencyMS int64   `json:"latencyMs"`
	TokensIn  int64   `json:"tokensIn"`
	TokensOut int64   `json:"tokensOut"`
	CostUSD   float64 `json:"costUsd"`
	Repairs   int     `json:"repairs"`
	ToolCalls int     `json:"toolCalls"`
}

// Report aggregates results.
type Report struct {
	StartedAt time.Time `json:"startedAt"`
	Results   []Result  `json:"results"`
}

// Summary renders per-model aggregates.
func (r *Report) Summary() []ModelSummary {
	byModel := map[string]*ModelSummary{}
	var order []string
	for _, res := range r.Results {
		s, ok := byModel[res.Model]
		if !ok {
			s = &ModelSummary{Model: res.Model}
			byModel[res.Model] = s
			order = append(order, res.Model)
		}
		s.Runs++
		s.TotalLatency += res.LatencyMS
		s.TotalTokens += res.TokensIn + res.TokensOut
		s.TotalCostUSD += res.CostUSD
		s.TotalRepairs += res.Repairs
		s.TotalToolCalls += res.ToolCalls
		if res.Passed {
			s.Passes++
		}
	}
	out := make([]ModelSummary, 0, len(order))
	for _, m := range order {
		s := byModel[m]
		s.SuccessRate = float64(s.Passes) / float64(s.Runs)
		s.AvgLatencyMS = s.TotalLatency / int64(s.Runs)
		out = append(out, *s)
	}
	return out
}

// ModelSummary aggregates one model's results.
type ModelSummary struct {
	Model          string  `json:"model"`
	Runs           int     `json:"runs"`
	Passes         int     `json:"passes"`
	SuccessRate    float64 `json:"successRate"`
	AvgLatencyMS   int64   `json:"avgLatencyMs"`
	TotalLatency   int64   `json:"totalLatencyMs"`
	TotalTokens    int64   `json:"totalTokens"`
	TotalCostUSD   float64 `json:"totalCostUsd"`
	TotalRepairs   int     `json:"totalRepairs"`
	TotalToolCalls int     `json:"totalToolCalls"`
}

// Runner executes benchmark cases.
type Runner struct {
	RT        *runtime.Runtime
	SessionID string
}

// Run executes all selected cases and models.
func (r *Runner) Run(ctx context.Context, opts RunOptions, cases []Case) (*Report, error) {
	if opts.Iterations <= 0 {
		opts.Iterations = 1
	}
	selected := cases
	if len(opts.Cases) > 0 {
		want := map[string]bool{}
		for _, id := range opts.Cases {
			want[id] = true
		}
		selected = nil
		for _, c := range cases {
			if want[c.ID] {
				selected = append(selected, c)
			}
		}
	}
	models := opts.Models

	report := &Report{StartedAt: time.Now().UTC()}
	for _, c := range selected {
		for _, m := range models {
			for i := 0; i < opts.Iterations; i++ {
				select {
				case <-ctx.Done():
					return report, ctx.Err()
				default:
				}
				res := r.runOnce(ctx, c, m, i+1, opts)
				report.Results = append(report.Results, res)
			}
		}
	}
	return report, nil
}

func (r *Runner) runOnce(ctx context.Context, c Case, modelRef string, iter int, opts RunOptions) Result {
	res := Result{CaseID: c.ID, Model: modelRef, Iteration: iter}
	start := time.Now()

	// Per-case scratch workspace for file-producing cases.
	ws := opts.Workspace
	if c.WorkspacePrep != nil {
		dir, err := makeScratchDir()
		if err != nil {
			res.Detail = "scratch: " + err.Error()
			return res
		}
		defer removeDir(dir)
		if err := c.WorkspacePrep(dir); err != nil {
			res.Detail = "prep: " + err.Error()
			return res
		}
		ws = dir
	}

	// Pin the model under test through the normal orchestrator.
	r.RT.Orchestrator.SetModelSelector(model.NewSelector(modelRef, nil))

	spec := task.Spec{
		Prompt: c.Prompt,
		CWD:    ws,
	}
	tsk, err := r.RT.Orchestrator.Run(ctx, r.SessionID, spec, "")
	res.LatencyMS = time.Since(start).Milliseconds()
	if err != nil {
		res.Detail = err.Error()
		return res
	}
	res.Strategy = tsk.Strategy
	res.Repairs = tsk.Repairs
	if tsk.Result != nil {
		res.TokensIn = tokensFor(r.RT, tsk.ID, "in")
		res.TokensOut = tokensFor(r.RT, tsk.ID, "out")
		res.CostUSD = costFor(r.RT, tsk.ID)
		res.ToolCalls = toolCallsFor(r.RT, r.SessionID, tsk.ID)
	}
	if c.Check != nil {
		pass, detail := c.Check(tsk)
		res.Passed = pass
		res.Detail = detail
	} else {
		res.Passed = tsk.Status == task.StatusCompleted
	}
	return res
}

func tokensFor(rt *runtime.Runtime, taskID, dir string) int64 {
	calls, _ := rt.Store.ModelCallsForTask(taskID)
	var total int64
	for _, c := range calls {
		if dir == "in" {
			total += c.TokensIn
		} else {
			total += c.TokensOut
		}
	}
	return total
}

func costFor(rt *runtime.Runtime, taskID string) float64 {
	calls, _ := rt.Store.ModelCallsForTask(taskID)
	var total float64
	for _, c := range calls {
		total += c.CostUSD
	}
	return total
}

func toolCallsFor(rt *runtime.Runtime, sessionID, taskID string) int {
	tcs, _ := rt.Store.ToolCalls(sessionID)
	var n int
	for _, tc := range tcs {
		if tc.TaskID == taskID {
			n++
		}
	}
	return n
}

func makeScratchDir() (string, error) {
	return os.MkdirTemp("", "omniharness-bench-*")
}

func removeDir(dir string) { os.RemoveAll(dir) }
