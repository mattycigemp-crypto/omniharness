package cli

import (
	"fmt"
	"io"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"omniharness/internal/session"
	"omniharness/internal/telemetry"
)

// statsReport is the --json shape. It is a flat record of what was measured,
// not a rendering of the table below, so a script does not have to parse
// columns to get at the numbers.
type statsReport struct {
	Totals telemetry.GlobalMetrics `json:"totals"`
	Models []telemetry.ModelStats  `json:"models"`
	Tools  []telemetry.ToolStats   `json:"tools"`
	// Strategies is its own field, not aligned with the three above: adding
	// it to that group would have shifted every one of their columns too.
	Strategies []telemetry.StrategyStats `json:"strategies"`
}

func newStatsCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "stats",
		Short: "Show usage across all sessions, by model and by tool",
		Long: "Report what has actually been spent and run: totals across every " +
			"session, then a breakdown per model and per tool.\n\n" +
			"Every figure comes from recorded rows. A store with no runs in it " +
			"reports zeroes rather than estimates.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			rt, err := newRuntime(cmd.Context())
			if err != nil {
				return err
			}
			defer rt.Close()

			report, err := collectStats(rt.Store)
			if err != nil {
				return err
			}
			if jsonOut {
				b, err := jsonMarshalIndent(report)
				if err != nil {
					return err
				}
				fmt.Println(string(b))
				return nil
			}
			printStats(os.Stdout, report)
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print the report as JSON")
	return cmd
}

// collectStats gathers the three views. A failure in any of them is returned
// rather than rendered as zeroes: a wrong number read as a right one is worse
// than no number.
func collectStats(store *session.Store) (statsReport, error) {
	var r statsReport
	var err error
	if r.Totals, err = telemetry.Global(store); err != nil {
		return r, fmt.Errorf("read totals: %w", err)
	}
	if r.Models, err = telemetry.ByModel(store); err != nil {
		return r, fmt.Errorf("read model breakdown: %w", err)
	}
	if r.Tools, err = telemetry.ByTool(store); err != nil {
		return r, fmt.Errorf("read tool breakdown: %w", err)
	}
	if r.Strategies, err = telemetry.ByStrategy(store); err != nil {
		return r, fmt.Errorf("read strategy breakdown: %w", err)
	}
	return r, nil
}

func printStats(out io.Writer, r statsReport) {
	t := r.Totals
	if t.Sessions == 0 && t.ModelCalls == 0 {
		fmt.Fprintln(out, "no runs recorded yet")
		return
	}

	fmt.Fprintf(out, "%s, %s (%d completed, %d failed), %s\n",
		count(t.Sessions, "session"), count(t.Tasks, "task"),
		t.Completed, t.Failed, count(int(t.Repairs), "repair"))
	fmt.Fprintf(out, "%s, %s, %d tokens, $%.4f, %s total\n",
		count(t.ModelCalls, "model call"), count(t.ToolCalls, "tool call"),
		t.Tokens, t.CostUSD, humanMS(t.TotalLatency))

	if len(r.Models) > 0 {
		fmt.Fprintln(out, "\nBY MODEL")
		w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "MODEL\tCALLS\tFAILED\tTOKENS\tCOST\tAVG")
		for _, m := range r.Models {
			fmt.Fprintf(w, "%s\t%d\t%d\t%d\t$%.4f\t%s\n",
				m.Model, m.Calls, m.Failed, m.TokensIn+m.TokensOut, m.CostUSD, humanMS(m.AvgMS))
		}
		_ = w.Flush()
	}

	if len(r.Tools) > 0 {
		fmt.Fprintln(out, "\nBY TOOL")
		w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "TOOL\tCALLS\tFAILED\tDENIED")
		for _, tl := range r.Tools {
			fmt.Fprintf(w, "%s\t%d\t%d\t%d\n", tl.Tool, tl.Calls, tl.Failed, tl.Denied)
		}
		_ = w.Flush()
	}

	// This is the same data memory.Advisor uses internally to decide whether
	// a strategy is underperforming enough to override — printed here so
	// that decision is checkable, not just trusted.
	if len(r.Strategies) > 0 {
		fmt.Fprintln(out, "\nBY STRATEGY")
		w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "STRATEGY\tRUNS\tFAILED\tREPAIRS\tCOST")
		for _, ss := range r.Strategies {
			fmt.Fprintf(w, "%s\t%d\t%d\t%d\t$%.4f\n", ss.Strategy, ss.Runs, ss.Failed, ss.Repairs, ss.CostUSD)
		}
		_ = w.Flush()
	}
}

// count renders "1 session" rather than "1 sessions". Only regular plurals
// appear here.
func count(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// humanMS renders a duration in the unit that reads without arithmetic.
func humanMS(ms int64) string {
	switch {
	case ms < 1000:
		return fmt.Sprintf("%dms", ms)
	case ms < 60_000:
		return fmt.Sprintf("%.1fs", float64(ms)/1000)
	default:
		return fmt.Sprintf("%dm%ds", ms/60_000, (ms%60_000)/1000)
	}
}
