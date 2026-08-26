package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"omniharness/internal/runtime"
	"omniharness/internal/task"
	"omniharness/internal/telemetry"
)

func newResumeCmd() *cobra.Command {
	var (
		headless bool
		jsonOut  bool
		taskID   string
	)
	cmd := &cobra.Command{
		Use:   "resume <session>",
		Short: "Resume a session's most recent task",
		Long: `Resumes the most recent task of a session. The task's profile, strategy and
artifacts are reused; agents continue from the stored transcript.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
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

			ss, err := rt.Store.GetSession(args[0])
			if err != nil {
				return fmt.Errorf("session %q not found: %w", args[0], err)
			}
			tasks, err := rt.Store.TasksBySession(ss.ID)
			if err != nil {
				return err
			}
			if len(tasks) == 0 {
				return fmt.Errorf("session %q has no tasks to resume", ss.ID)
			}
			tsk := tasks[0]
			if taskID != "" {
				for _, t := range tasks {
					if t.ID == taskID {
						tsk = t
						break
					}
				}
			}

			stop := streamEvents(rt, ss.ID, headless)
			defer stop()

			resumed, err := rt.RunTask(cmd.Context(), ss.ID, tsk.Spec.Prompt, runtime.RunOptions{
				TaskID:     tsk.ID,
				ApproveAll: rootOpts.Yes,
			})
			if err != nil {
				return fmt.Errorf("resume failed: %w", err)
			}
			if jsonOut {
				return printTaskJSON(resumed)
			}
			printTaskResult(resumed)
			return nil
		},
	}
	cmd.Flags().BoolVar(&headless, "headless", false, "print progress to stderr instead of using the TUI")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print the task result as JSON")
	cmd.Flags().StringVar(&taskID, "task", "", "resume a specific task id instead of the newest")
	return cmd
}

func newSessionsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "sessions",
		Short: "List sessions and their metrics",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			rt, err := newRuntime(cmd.Context())
			if err != nil {
				return err
			}
			defer rt.Close()
			sessions, err := rt.ListSessions(50)
			if err != nil {
				return err
			}
			if len(sessions) == 0 {
				fmt.Println("no sessions yet")
				return nil
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tSTATUS\tTITLE\tTASKS\tTOKENS\tCOST\tCREATED")
			for _, ss := range sessions {
				tasks, _ := rt.Store.TasksBySession(ss.ID)
				m, _ := telemetry.ForSession(rt.Store, ss.ID)
				fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%d\t$%.4f\t%s\n",
					shortID(ss.ID), ss.Status, truncate(ss.Title, 40), len(tasks),
					m.TokensIn+m.TokensOut, m.CostUSD, ss.CreatedAt.Local().Format("2006-01-02 15:04"))
			}
			return w.Flush()
		},
	}
}

func newLogCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "log <session>",
		Short: "Print the persisted event log of a session",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rt, err := newRuntime(cmd.Context())
			if err != nil {
				return err
			}
			defer rt.Close()
			evs, err := rt.SessionEvents(args[0], 5000)
			if err != nil {
				return fmt.Errorf("load events: %w", err)
			}
			if len(evs) == 0 {
				fmt.Println("no events for this session")
				return nil
			}
			for _, e := range evs {
				fmt.Printf("%s %-24s %s\n", e.Time.Local().Format("15:04:05.000"), e.Type, compactPayload(e.Data))
			}
			return nil
		},
	}
}

func compactPayload(data json.RawMessage) string {
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return string(data)
	}
	b, _ := json.Marshal(m)
	if len(b) > 200 {
		return string(b[:200]) + "…"
	}
	return string(b)
}

var _ = task.StatusCompleted
var _ = time.Now
