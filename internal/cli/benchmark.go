package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"omniharness/internal/benchmark"
)

func newBenchmarkCmd() *cobra.Command {
	var (
		models      []string
		cases       []string
		iterations  int
		jsonOut     bool
		sessionID   string
	)
	cmd := &cobra.Command{
		Use:   "benchmark",
		Short: "Compare models and strategies on repeatable tasks",
		Long: `Runs built-in benchmark tasks through the normal orchestrator and reports
machine-readable results. Use --models to pin specific provider/model refs.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			rt, err := newRuntime(cmd.Context())
			if err != nil {
				return err
			}
			defer rt.Close()
			installApprover(rt, rootOpts.Yes)
			cfg, _ := loadConfig()

			if sessionID == "" {
				ss, err := rt.NewSession("", "benchmark")
				if err != nil {
					return err
				}
				sessionID = ss.ID
			}

			if len(models) == 0 {
				models = []string{cfg.Models.Default}
			}

			runner := &benchmark.Runner{RT: rt, SessionID: sessionID}
			ctx, cancel := context.WithCancel(cmd.Context())
			defer cancel()
			installInterrupt(cancel)

			fmt.Fprintf(os.Stderr, "benchmark: %d case(s) × %d model(s) × %d iteration(s)\n",
				len(pickCases(cases)), len(models), iterationsOrDefault(iterations))

			report, err := runner.Run(ctx, benchmark.RunOptions{
				Models:     models,
				Cases:      cases,
				Iterations: iterations,
			}, benchmark.BuiltinCases())
			if err != nil {
				return err
			}
			if jsonOut {
				b, err := json.MarshalIndent(report, "", "  ")
				if err != nil {
					return err
				}
				fmt.Println(string(b))
				return nil
			}
			printBenchmarkReport(report)
			return nil
		},
	}
	cmd.Flags().StringSliceVar(&models, "models", nil, "provider/model refs to benchmark (default: config default)")
	cmd.Flags().StringSliceVar(&cases, "cases", nil, "benchmark case ids (default: all)")
	cmd.Flags().IntVar(&iterations, "iterations", 1, "iterations per case")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print machine-readable JSON")
	cmd.Flags().StringVar(&sessionID, "session", "", "attach results to an existing session")
	return cmd
}

func iterationsOrDefault(n int) int {
	if n <= 0 {
		return 1
	}
	return n
}

func pickCases(ids []string) []benchmark.Case {
	cases := benchmark.BuiltinCases()
	if len(ids) == 0 {
		return cases
	}
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	var out []benchmark.Case
	for _, c := range cases {
		if want[c.ID] {
			out = append(out, c)
		}
	}
	return out
}

func printBenchmarkReport(r *benchmark.Report) {
	fmt.Println()
	fmt.Println("── benchmark results ─────────────────────────")
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "MODEL\tRUNS\tPASS\tRATE\tAVG MS\tTOKENS\tCOST\tREPAIRS")
	for _, s := range r.Summary() {
		fmt.Fprintf(w, "%s\t%d\t%d\t%.0f%%\t%d\t%d\t$%.4f\t%d\n",
			s.Model, s.Runs, s.Passes, s.SuccessRate*100, s.AvgLatencyMS,
			s.TotalTokens, s.TotalCostUSD, s.TotalRepairs)
	}
	w.Flush()
	fmt.Println()
	for _, res := range r.Results {
		mark := "✓"
		if !res.Passed {
			mark = "✗"
		}
		fmt.Printf("%s %s %s iter=%d %s %dms %s\n", mark, res.Model, res.CaseID, res.Iteration,
			res.Strategy, res.LatencyMS, truncate(res.Detail, 80))
	}
	_ = time.Now
}
