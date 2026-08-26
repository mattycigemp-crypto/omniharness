package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"omniharness/internal/combo"
	"omniharness/internal/config"
	"omniharness/internal/gateway"
)

// comboCatalog fetches the live OmniRoute catalog (ids only), falling back to
// the static combo list when the server is unreachable. The fallback is
// labeled so the user knows the list is offline.
func comboCatalog(ctx context.Context, cfg config.Config) ([]combo.Option, bool, error) {
	gw := gateway.New(cfg.OmniRoute.Endpoint, cfg.OmniRoute.Timeout, cfg.OmniRoute.APIKey)
	fetchCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	ids, err := gw.ListCatalog(fetchCtx)
	if err != nil {
		return combo.List(nil), false, nil
	}
	return combo.List(ids), true, nil
}

// newStackCmd builds `omniharness stack`: list the model combos the harness
// can route through, show the configured one, and set it (persisted to the
// config file). A "combo" is either an OmniRoute auto/* routing combo or a
// specific provider/model id.
func newStackCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "stack",
		Short: "Choose the model combo the harness routes through",
		Long: "Choose the model combo the harness routes through. A combo is\n" +
			"either an OmniRoute auto/* routing combo (resolves to whatever\n" +
			"provider is provisioned) or a specific provider/model id.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			opts, live, err := comboCatalog(cmd.Context(), cfg)
			if err != nil {
				return err
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintf(w, "COMBO\tDESCRIPTION\tSTATUS\n")
			for _, o := range opts {
				status := ""
				if o.ID == cfg.Models.Default {
					status = "current"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\n", o.ID, o.Description, status)
			}
			w.Flush()
			if !live {
				fmt.Fprintln(os.Stderr, "note: OmniRoute catalog unreachable — showing built-in combos; `stack set` still accepts any provider/model id")
			}
			fmt.Printf("\ncurrent combo: %s\nset: omniharness stack set <combo>\n", cfg.Models.Default)
			return nil
		},
	}
	cmd.AddCommand(
		&cobra.Command{
			Use:   "show",
			Short: "Show the configured model combo",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				cfg, err := loadConfig()
				if err != nil {
					return err
				}
				fmt.Printf("combo: %s\n", cfg.Models.Default)
				fmt.Printf("hint:  %s\n", combo.Describe(cfg.Models.Default))
				if len(cfg.Models.Capabilities) > 0 {
					fmt.Println("\ncapability defaults (used when the task profile asks):")
					keys := make([]string, 0, len(cfg.Models.Capabilities))
					for k := range cfg.Models.Capabilities {
						keys = append(keys, k)
					}
					sort.Strings(keys)
					for _, k := range keys {
						v := cfg.Models.Capabilities[k]
						if v == "" {
							v = "(falls back to combo)"
						}
						fmt.Printf("  %-14s %s\n", k, v)
					}
				}
				return nil
			},
		},
		&cobra.Command{
			Use:   "set <combo>",
			Short: "Set the model combo (persisted to the config file)",
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				id := args[0]
				path := rootOpts.ConfigPath
				if path == "" {
					path = config.DefaultPath()
				}
				cfg, err := config.Load(path)
				if err != nil {
					return err
				}
				// Validate against the live catalog when reachable; otherwise
				// accept any well-formed provider/model id.
				opts, live, err := comboCatalog(cmd.Context(), cfg)
				if err != nil {
					return err
				}
				known := false
				for _, o := range opts {
					if o.ID == id {
						known = true
						break
					}
				}
				if live && !known {
					// Still allow auto/* (the catalog may lag a freshly
					// configured combo) — OmniRoute accepts unknown combos
					// and the repair loop handles routing failures.
					if !combo.IsAuto(id) {
						return fmt.Errorf("%s (use `omniharness stack` to list available combos)", combo.FormatError(id))
					}
				}
				if !live && !combo.IsAuto(id) && !strings.Contains(id, "/") {
					return fmt.Errorf("%s", combo.FormatError(id))
				}
				cfg.Models.Default = id
				if err := cfg.Save(path); err != nil {
					return err
				}
				fmt.Printf("combo set to %q (%s)\n", id, combo.Describe(id))
				fmt.Printf("config: %s\n", filepath.Clean(path))
				return nil
			},
		},
	)
	return cmd
}
