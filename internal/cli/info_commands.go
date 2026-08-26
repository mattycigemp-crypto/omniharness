package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"text/tabwriter"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/spf13/cobra"

	"omniharness/internal/config"
	"omniharness/internal/gateway"
	"omniharness/internal/telemetry"
	"omniharness/internal/version"
)

func newModelsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "models",
		Short: "Inspect OmniRoute providers and models",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			rt, err := newRuntime(cmd.Context())
			if err != nil {
				return err
			}
			defer rt.Close()

			ctx, cancel := context.WithTimeout(cmd.Context(), 20*time.Second)
			defer cancel()
			providers, err := rt.Gateway.ListProviders(ctx)
			if err != nil {
				var ge *gateway.Error
				if errors.As(err, &ge) && ge.Kind == gateway.KindAuth {
					return fmt.Errorf("list providers requires an OmniRoute API key; set OMNIROUTE_API_KEY (see `omniharness doctor`)")
				}
				return fmt.Errorf("list providers: %w", err)
			}
			fmt.Printf("OmniRoute endpoint: %s\n", cfg.OmniRoute.Endpoint)
			if len(providers) == 0 {
				fmt.Println("no providers reported")
				return nil
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "PROVIDER\tSTATUS")
			for _, p := range providers {
				fmt.Fprintf(w, "%s\t%s\n", p.Name, p.Status)
			}
			w.Flush()

			// Catalog models for the first provider that answers.
			for _, p := range providers {
				models, err := rt.Gateway.ListModels(ctx, p.ID)
				if err != nil {
					continue
				}
				if len(models) > 0 {
					fmt.Printf("\nModels for %s:\n", p.Name)
					w2 := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
					for _, m := range models {
						fmt.Fprintf(w2, "  %s\t%s\n", m.ID, m.Name)
					}
					w2.Flush()
				}
			}
			return nil
		},
	}
}

func newDoctorCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Run diagnostics on the installation",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			results := []diagResult{}
			check := func(name string, ok bool, detail string) {
				results = append(results, diagResult{Name: name, OK: ok, Detail: detail})
				mark := "✓"
				if !ok {
					mark = "✗"
				}
				fmt.Printf("%s %-28s %s\n", mark, name, detail)
			}

			check("config", err == nil, "default config valid")
			if err := cfg.Validate(); err != nil {
				check("config validation", false, err.Error())
			} else {
				check("config validation", true, "valid")
			}
			if _, err := os.Stat(cfg.Persistence.Dir); err == nil {
				check("persistence dir", true, cfg.Persistence.Dir)
			} else {
				if err := os.MkdirAll(cfg.Persistence.Dir, 0o755); err != nil {
					check("persistence dir", false, err.Error())
				} else {
					check("persistence dir", true, cfg.Persistence.Dir+" (created)")
				}
			}

			// Go is only needed to rebuild from source. For binaries installed
			// from the npm package it is not a requirement, so don't fail the
			// report over it.
			if fromNpm, _ := os.Executable(); installedFromNpm(filepath.Clean(fromNpm)) {
				check("go toolchain", true, "not needed (npm install)")
			} else if _, err := exec.LookPath("go"); err == nil {
				check("go toolchain", true, "found")
			} else {
				check("go toolchain", false, "not on PATH (needed to rebuild from source)")
			}
			if _, err := exec.LookPath("git"); err == nil {
				check("git", true, "found")
			} else {
				check("git", false, "not on PATH")
			}

			rt, err := newRuntime(cmd.Context())
			if err != nil {
				check("runtime", false, err.Error())
			} else {
				check("runtime", true, "wired")
				defer rt.Close()
			}

			// OmniRoute's /v1/models can take tens of seconds to rebuild its
			// catalog after idle (observed >25s), so give the probe real budget
			// instead of misreporting a live server as unreachable.
			ctx, cancel := context.WithTimeout(cmd.Context(), 45*time.Second)
			defer cancel()
			diag := rt.Gateway.Diagnose(ctx)

			// Endpoint reachability, independent of auth.
			switch diag.State {
			case gateway.AuthUnreachable:
				check("omniroute endpoint", false, fmt.Sprintf("%s unreachable (%s)", cfg.OmniRoute.Endpoint, diag.Detail))
			default:
				check("omniroute endpoint", true, fmt.Sprintf("%s reachable (HTTP %d)", cfg.OmniRoute.Endpoint, diag.Status))
			}

			// Authentication status. The credential itself is never printed:
			// only whether one is set (masked to its last 4 characters, the
			// same convention OmniRoute's own logs use) and the verdict.
			keyMask := "not set"
			if cfg.OmniRoute.APIKey != "" {
				keyMask = "key_" + last4(cfg.OmniRoute.APIKey)
			}
			authOK := diag.State == gateway.AuthOK || diag.State == gateway.AuthNotRequired
			check("omniroute auth", authOK, fmt.Sprintf("%s [%s] (%s)", authLabel(diag.State), keyMask, diag.Detail))

			g, err := telemetry.Global(rt.Store)
			if err == nil {
				check("store", true, fmt.Sprintf("%d sessions, %d tasks, %d model calls", g.Sessions, g.Tasks, g.ModelCalls))
			} else {
				check("store", false, err.Error())
			}

			check("version", true, version.String())

			failures := 0
			for _, r := range results {
				if !r.OK {
					failures++
				}
			}
			fmt.Printf("\n%d of %d checks passed\n", len(results)-failures, len(results))
			if jsonOut {
				b, _ := jsonMarshalIndent(results)
				fmt.Println(string(b))
			}
			if failures > 0 {
				return fmt.Errorf("%d checks failed", failures)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print results as JSON")
	return cmd
}

type diagResult struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

func jsonMarshalIndent(v any) ([]byte, error) {
	return json.MarshalIndent(v, "", "  ")
}

// last4 returns the final 4 characters of s (used to mask credentials).
func last4(s string) string {
	if len(s) <= 4 {
		return "****"
	}
	return s[len(s)-4:]
}

// authLabel renders a human-readable label for an auth state. It never
// includes credential material.
func authLabel(s gateway.AuthState) string {
	switch s {
	case gateway.AuthOK:
		return "authenticated"
	case gateway.AuthNotRequired:
		return "auth not required"
	case gateway.AuthNotConfigured:
		return "auth required, key missing"
	case gateway.AuthRejected:
		return "key rejected"
	case gateway.AuthUnreachable:
		return "unreachable"
	case gateway.AuthMisconfigured:
		return "misconfigured"
	}
	return string(s)
}

func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Manage configuration",
	}
	cmd.AddCommand(
		&cobra.Command{
			Use:   "show",
			Short: "Show the effective configuration",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				cfg, err := loadConfig()
				if err != nil {
					return err
				}
				// Never print secrets.
				cfg.OmniRoute.APIKey = "***"
				enc := toml.NewEncoder(os.Stdout)
				return enc.Encode(cfg)
			},
		},
		&cobra.Command{
			Use:   "init",
			Short: "Write a default config file to ~/.omniharness.toml",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				path := rootOpts.ConfigPath
				if path == "" {
					path = config.DefaultPath()
				}
				if err := config.WriteDefault(path); err != nil {
					return err
				}
				fmt.Printf("wrote default config to %s\n", path)
				return nil
			},
		},
	)
	return cmd
}

func newPluginsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "plugins",
		Short: "List loaded tools and MCP servers",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			rt, err := newRuntime(cmd.Context())
			if err != nil {
				return err
			}
			defer rt.Close()

			fmt.Println("configured MCP servers:")
			if len(cfg.MCP.Servers) == 0 {
				fmt.Println("  (none — add [[mcp.servers]] entries to your config)")
			}
			for _, s := range cfg.MCP.Servers {
				line := fmt.Sprintf("  %s → %s %s", s.Name, s.Command, s.Args)
				fmt.Println(line)
			}

			fmt.Println("\nnative tools:")
			for _, spec := range rt.Tools.List() {
				fmt.Printf("  %-16s [%s] %s\n", spec.Name, spec.Risk, spec.Description)
			}
			return nil
		},
	}
}
