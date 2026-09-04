package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"omniharness/internal/budget"
	"omniharness/internal/event"
	"omniharness/internal/runtime"
	"omniharness/internal/task"
)

func newRunCmd() *cobra.Command {
	var (
		prompt       string
		promptFile   string
		sessionID    string
		headless     bool
		jsonOut      bool
		cwd          string
		maxTokens    int64
		maxCost      float64
		maxDuration  time.Duration
		maxToolCalls int
	)
	cmd := &cobra.Command{
		Use:   "run [prompt]",
		Short: "Analyze and execute a task",
		Long: `Analyzes a task, selects a strategy, orchestrates agents and routes model
execution through OmniRoute. The prompt may be given as an argument, with
--prompt, or read from a file with --file.`,
		Example: `  omniharness run "fix the failing test in ./internal/task"
  omniharness run --file task.md --max-cost 0.50
  git show HEAD | omniharness run --headless -
  omniharness --yes run "bump the version and tag it"`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := resolvePrompt(prompt, promptFile, args)
			if err != nil {
				return err
			}
			rt, err := newRuntime(cmd.Context())
			if err != nil {
				return err
			}
			defer rt.Close()
			installApprover(rt, rootOpts.Yes)
			cfg, _ := loadConfig()
			if err := ensureAuth(cmd.Context(), rt, cfg, authPrompter{interactive: isTerminal(os.Stdin), stdin: os.Stdin}); err != nil {
				return err
			}
			loadMCPServersFromConfig(cmd.Context(), rt, cfg)

			if sessionID == "" {
				title := truncate(p, 60)
				ss, err := rt.NewSession(cwd, title)
				if err != nil {
					return fmt.Errorf("create session: %w", err)
				}
				sessionID = ss.ID
			}

			stop := streamEvents(rt, sessionID, headless)
			defer stop()

			runCtx, cancel := context.WithCancel(cmd.Context())
			defer cancel()
			installInterrupt(cancel)

			tsk, err := rt.RunTask(runCtx, sessionID, p, runtime.RunOptions{
				Budget: budget.Budget{
					MaxTokens:    maxTokens,
					MaxCostUSD:   maxCost,
					MaxDuration:  maxDuration,
					MaxToolCalls: maxToolCalls,
				},
				Deadline:   maxDuration,
				ApproveAll: rootOpts.Yes,
			})
			if err != nil {
				if runCtx.Err() == context.Canceled {
					return fmt.Errorf("task cancelled")
				}
				return fmt.Errorf("task failed: %w", err)
			}
			if jsonOut {
				return printTaskJSON(tsk)
			}
			printTaskResult(tsk)
			return nil
		},
	}
	cmd.Flags().StringVarP(&prompt, "prompt", "p", "", "task prompt")
	cmd.Flags().StringVarP(&promptFile, "file", "f", "", "read the prompt from a file")
	cmd.Flags().StringVar(&sessionID, "session", "", "attach to an existing session id")
	cmd.Flags().BoolVar(&headless, "headless", false, "run without the TUI, printing progress to stderr")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print the task result as JSON")
	cmd.Flags().StringVar(&cwd, "cwd", "", "working directory (default: current)")
	cmd.Flags().Int64Var(&maxTokens, "max-tokens", 0, "token budget")
	cmd.Flags().Float64Var(&maxCost, "max-cost", 0, "estimated cost budget (USD)")
	cmd.Flags().DurationVar(&maxDuration, "max-duration", 0, "wall-clock budget")
	cmd.Flags().IntVar(&maxToolCalls, "max-tool-calls", 0, "tool-call budget")
	return cmd
}

func resolvePrompt(prompt, promptFile string, args []string) (string, error) {
	if len(args) > 0 && args[0] != "-" {
		return args[0], nil
	}
	if prompt != "" {
		return prompt, nil
	}
	if promptFile != "" {
		b, err := os.ReadFile(promptFile)
		if err != nil {
			return "", fmt.Errorf("read prompt file: %w", err)
		}
		return strings.TrimSpace(string(b)), nil
	}
	// Read stdin when the caller passed "-" or piped input in.
	if (len(args) > 0 && args[0] == "-") || !isTerminal(os.Stdin) {
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			return "", fmt.Errorf("read prompt from stdin: %w", err)
		}
		if s := strings.TrimSpace(string(b)); s != "" {
			return s, nil
		}
	}
	return "", fmt.Errorf("no prompt given: pass it as an argument, use --prompt / --file, or pipe it on stdin")
}

// installInterrupt cancels the context on SIGINT/SIGTERM.
func installInterrupt(cancel context.CancelFunc) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-ch
		cancel()
	}()
}

// streamEvents prints a compact live view of runtime events when headless.
func streamEvents(rt *runtime.Runtime, sessionID string, headless bool) func() {
	if !headless {
		return func() {}
	}
	ch, cancel := rt.Bus.Subscribe(512)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for e := range ch {
			if e.SessionID != sessionID {
				continue
			}
			if line := formatEvent(e); line != "" {
				fmt.Fprintln(os.Stderr, line)
			}
		}
	}()
	return func() {
		cancel()
		<-done
	}
}

// formatEvent renders an event as a compact headless log line.
func formatEvent(e event.Event) string {
	switch e.Type {
	case event.TaskCreated:
		return "task created"
	case event.TaskAnalyzed:
		var d event.TaskAnalyzedData
		_ = json.Unmarshal(e.Data, &d)
		return fmt.Sprintf("analyzed: %s/%s risk=%s ctx=%s", d.Profile.Domain, d.Profile.Complexity, d.Profile.Risk, d.Profile.Context)
	case event.StrategySelected:
		var d event.StrategySelectedData
		_ = json.Unmarshal(e.Data, &d)
		return fmt.Sprintf("strategy: %s (%s)", d.Strategy, d.Reason)
	case event.AgentCreated:
		var d event.AgentCreatedData
		_ = json.Unmarshal(e.Data, &d)
		return fmt.Sprintf("agent[%s] role=%s model=%s", shortID(e.AgentID), d.Role, d.Model)
	case event.AgentUpdated:
		var d event.AgentStateData
		_ = json.Unmarshal(e.Data, &d)
		return fmt.Sprintf("agent[%s] %s %s", shortID(e.AgentID), d.Status, d.Action)
	case event.AgentCompleted:
		return fmt.Sprintf("agent[%s] completed", shortID(e.AgentID))
	case event.AgentFailed:
		var d event.AgentFailedData
		_ = json.Unmarshal(e.Data, &d)
		return fmt.Sprintf("agent[%s] failed: %s", shortID(e.AgentID), truncate(d.Error, 120))
	case event.ModelRequested:
		var d event.ModelRequestedData
		_ = json.Unmarshal(e.Data, &d)
		return fmt.Sprintf("model request %s", d.Model)
	case event.ModelResponded:
		var d event.ModelRespondedData
		_ = json.Unmarshal(e.Data, &d)
		return fmt.Sprintf("model reply   %s (in=%d out=%d $%.4f %s)", d.Model, d.TokensIn, d.TokensOut, d.CostUSD, formatDuration(d.Latency))
	case event.ModelFailed:
		var d event.ModelFailedData
		_ = json.Unmarshal(e.Data, &d)
		return fmt.Sprintf("model failed  %s: %s", d.Model, truncate(d.Error, 120))
	case event.ToolRequested:
		var d event.ToolRequestedData
		_ = json.Unmarshal(e.Data, &d)
		return fmt.Sprintf("tool request %s [%s] %s", d.Tool, d.Risk, truncate(d.Input, 100))
	case event.ToolCompleted:
		var d event.ToolFinishedData
		_ = json.Unmarshal(e.Data, &d)
		// ToolFinishedData carries the outcome in Status; printing "ok" for
		// every one of them reported policy denials as successes, so a
		// headless run said work had happened that had not.
		switch d.Status {
		case "denied":
			if d.Error != "" {
				return fmt.Sprintf("tool denied  %s: %s", d.Tool, truncate(d.Error, 120))
			}
			return fmt.Sprintf("tool denied  %s", d.Tool)
		case "failed":
			return fmt.Sprintf("tool failed  %s: %s", d.Tool, truncate(d.Error, 120))
		case "cancelled":
			return fmt.Sprintf("tool cancel  %s", d.Tool)
		default:
			return fmt.Sprintf("tool ok      %s (%s)", d.Tool, formatDuration(d.Duration))
		}
	case event.ToolFailed:
		var d event.ToolFailedData
		_ = json.Unmarshal(e.Data, &d)
		return fmt.Sprintf("tool failed  %s: %s", d.Tool, truncate(d.Error, 120))
	case event.EvaluationStarted:
		var d event.EvaluationData
		_ = json.Unmarshal(e.Data, &d)
		return fmt.Sprintf("evaluate: %s", d.Evaluator)
	case event.EvaluationComplete:
		var d event.EvaluationCompletedData
		_ = json.Unmarshal(e.Data, &d)
		return fmt.Sprintf("evaluate: %s -> %s", d.Evaluator, d.Outcome)
	case event.RepairStarted:
		var d event.RepairData
		_ = json.Unmarshal(e.Data, &d)
		return fmt.Sprintf("repair #%d: %s (changed: %s)", d.Attempt, d.Strategy, strings.Join(d.Changed, ", "))
	case event.TaskCompleted:
		return "task completed"
	case event.TaskFailed:
		var d event.TaskFailedData
		_ = json.Unmarshal(e.Data, &d)
		return fmt.Sprintf("task failed: %s", truncate(d.Error, 200))
	case event.TaskCancelled:
		return "task cancelled"
	case event.BudgetExceeded:
		var d event.BudgetExceededData
		_ = json.Unmarshal(e.Data, &d)
		return fmt.Sprintf("budget exceeded: %s", d.Dimension)
	default:
		return ""
	}
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func printTaskResult(tsk *task.Task) {
	fmt.Println()
	fmt.Println("── task result ───────────────────────────────")
	fmt.Printf("session:  %s\n", tsk.SessionID)
	fmt.Printf("task:     %s\n", tsk.ID)
	fmt.Printf("status:   %s\n", tsk.Status)
	fmt.Printf("strategy: %s\n", tsk.Strategy)
	fmt.Printf("repairs:  %d\n", tsk.Repairs)
	if tsk.Result != nil {
		if len(tsk.Result.Artifacts) > 0 {
			fmt.Printf("artifacts:\n")
			for _, a := range tsk.Result.Artifacts {
				fmt.Printf("  - %s\n", a)
			}
		}
		if tsk.Result.Output != "" {
			fmt.Println()
			fmt.Println(tsk.Result.Output)
		}
	}
}

func printTaskJSON(tsk *task.Task) error {
	b, err := json.MarshalIndent(tsk, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(b))
	return nil
}
