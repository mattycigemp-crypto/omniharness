// Package cli implements the OmniHarness command line interface. The CLI is
// fully usable headless — the TUI is a separate consumer of the same runtime.
package cli

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"

	"omniharness/internal/config"
	"omniharness/internal/mcp"
	"omniharness/internal/policy"
	"omniharness/internal/runtime"
	"omniharness/internal/tui"
	"omniharness/internal/version"
)

// RootOptions carry global flags.
type RootOptions struct {
	ConfigPath string
	DataDir    string
	Endpoint   string
	LogLevel   string
	Workspace  string
	Yes        bool // approve-all for the run commands
}

var rootOpts RootOptions

// NewRootCmd builds the root command.
func NewRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "omniharness",
		Short: "Agent orchestration above OmniRoute",
		Long: `OmniHarness is an agent orchestration system that analyzes tasks, selects
execution strategies, orchestrates capability-driven agents, and routes model
execution through OmniRoute. It is local-first and headless-capable.`,
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       version.Short(), // cobra prefixes the program name itself
		Args:          cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Bare `omniharness` opens the TUI when attached to a terminal,
			// otherwise shows help.
			if isTerminal(os.Stdout) {
				return runTUI(cmd.Context(), rootOpts)
			}
			return cmd.Help()
		},
	}
	root.PersistentFlags().StringVar(&rootOpts.ConfigPath, "config", "", "path to config file (default ~/.omniharness.toml)")
	root.PersistentFlags().StringVar(&rootOpts.DataDir, "data-dir", "", "override persistence directory")
	root.PersistentFlags().StringVar(&rootOpts.Endpoint, "endpoint", "", "override OmniRoute endpoint")
	root.PersistentFlags().StringVar(&rootOpts.LogLevel, "log-level", "", "override log level (debug|info|warn|error)")
	root.PersistentFlags().StringVar(&rootOpts.Workspace, "workspace", "", "directory the agent may touch (default: current directory)")
	root.PersistentFlags().BoolVar(&rootOpts.Yes, "yes", false, "auto-approve high-risk actions (use with care)")

	root.AddCommand(
		newRunCmd(),
		newResumeCmd(),
		newSessionsCmd(),
		newModelsCmd(),
		newDoctorCmd(),
		newConfigCmd(),
		newPluginsCmd(),
		newServeCmd(),
		newBenchmarkCmd(),
		newStartCmd(),
		newTUISubCmd(),
		newLogCmd(),
		newUpdateCmd(),
		newStackCmd(),
	)
	return root
}

// newStartCmd is the single explicit command to launch the harness: it runs
// the interactive auth pre-flight (prompting for a pasted API key when none is
// configured) and opens the TUI cockpit. Bare `omniharness` does the same when
// attached to a terminal.
func newStartCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "start",
		Short: "Start the harness: authenticate (prompts for a key if needed) and open the cockpit",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTUI(cmd.Context(), rootOpts)
		},
	}
}

func newTUISubCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "tui",
		Short: "Launch the terminal UI",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTUI(cmd.Context(), rootOpts)
		},
	}
}

// loadConfig resolves the effective configuration from flags + file + env.
func loadConfig() (config.Config, error) {
	path := rootOpts.ConfigPath
	if path == "" {
		path = config.DefaultPath()
	}
	cfg, err := config.Load(path)
	if err != nil {
		return cfg, err
	}
	if rootOpts.DataDir != "" {
		cfg.Persistence.Dir = rootOpts.DataDir
	}
	if rootOpts.Endpoint != "" {
		cfg.OmniRoute.Endpoint = rootOpts.Endpoint
	}
	if rootOpts.LogLevel != "" {
		cfg.Logging.Level = rootOpts.LogLevel
	}
	if rootOpts.Workspace != "" {
		abs, err := filepath.Abs(rootOpts.Workspace)
		if err != nil {
			return cfg, fmt.Errorf("--workspace %s: %w", rootOpts.Workspace, err)
		}
		cfg.Policy.WorkspaceRoot = abs
	}
	return cfg, nil
}

// newRuntime builds a wired runtime from flags + config.
func newRuntime(ctx context.Context) (*runtime.Runtime, error) {
	cfg, err := loadConfig()
	if err != nil {
		return nil, err
	}
	return runtime.New(cfg, runtime.Options{})
}

// installApprover wires human approval handling into the runtime.
func installApprover(rt *runtime.Runtime, approveAll bool) {
	rt.SetApprover(policy.ApproverFunc(func(ctx context.Context, r policy.Request, reason string) (bool, error) {
		if approveAll {
			return true, nil
		}
		if !isTerminal(os.Stdin) {
			fmt.Fprintf(os.Stderr, "denied: tool %q (%s risk) needs approval and stdin is not a terminal\n   reason: %s\n   re-run with --yes to auto-approve\n",
				r.Tool, r.Risk, reason)
			return false, nil
		}
		fmt.Fprintf(os.Stderr, "\napproval required: tool %q (%s risk)\n   reason: %s\n   approve? [y/N] ", r.Tool, r.Risk, reason)
		reader := bufio.NewReader(os.Stdin)
		line, _ := reader.ReadString('\n')
		line = strings.ToLower(strings.TrimSpace(line))
		return line == "y" || line == "yes", nil
	}))
}

// runTUI launches the Bubble Tea cockpit. It pre-flights authentication
// (prompting for a pasted API key when none is configured), then hands the
// runtime to the TUI.
func runTUI(ctx context.Context, opts RootOptions) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	rt, err := runtime.New(cfg, runtime.Options{})
	if err != nil {
		return fmt.Errorf("runtime: %w", err)
	}
	defer rt.Close()
	if err := ensureAuth(ctx, rt, cfg, authPrompter{interactive: isTerminal(os.Stdin), stdin: os.Stdin}); err != nil {
		return err
	}
	configPath := rootOpts.ConfigPath
	if configPath == "" {
		configPath = config.DefaultPath()
	}
	return tui.Run(cfg, rt, configPath)
}

// isTerminal reports whether f is attached to an interactive terminal.
// go-isatty uses the platform-native check (GetConsoleMode on Windows), which
// is reliable where ModeCharDevice misdetects MSYS pipes and /dev/null.
func isTerminal(f *os.File) bool {
	return isatty.IsTerminal(f.Fd())
}

// loadMCPServersFromConfig starts MCP servers configured in the config file.
func loadMCPServersFromConfig(ctx context.Context, rt *runtime.Runtime, cfg config.Config) {
	if len(cfg.MCP.Servers) == 0 {
		return
	}
	servers := make([]mcp.Server, 0, len(cfg.MCP.Servers))
	for _, s := range cfg.MCP.Servers {
		servers = append(servers, mcp.Server{Name: s.Name, Command: s.Command, Args: s.Args, Env: s.Env})
	}
	if err := rt.LoadMCPServers(ctx, servers); err != nil {
		fmt.Fprintf(os.Stderr, "warning: %v\n", err)
	}
}

// formatDuration renders durations compactly.
func formatDuration(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
}

// truncate shortens long strings for tables.
func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

var _ = filepath.Join
