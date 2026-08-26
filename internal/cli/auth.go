package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"omniharness/internal/config"
	"omniharness/internal/gateway"
	"omniharness/internal/runtime"
)

// authPrompter controls interactive key entry. interactive is true when stdin
// is a real terminal; stdin is the reader to consume the pasted key from.
type authPrompter struct {
	interactive bool
	stdin       io.Reader
}

// ensureAuth makes sure the runtime has an OmniRoute API key before the
// harness starts. It never fails when a key is already configured; when the
// server runs in anonymous mode (REQUIRE_API_KEY off) no key is needed at all.
// Otherwise, on an interactive terminal the user is asked to paste their key
// (held in memory only, never persisted); non-interactive invocations get an
// actionable error instead. The credential is never printed — only its last
// four characters, masked as key_<last4>.
func ensureAuth(ctx context.Context, rt *runtime.Runtime, cfg config.Config, in authPrompter) error {
	if cfg.OmniRoute.APIKey != "" {
		return nil
	}

	diag := rt.Gateway.Diagnose(ctx)
	switch diag.State {
	case gateway.AuthNotRequired:
		fmt.Fprintln(os.Stderr, "note: OmniRoute accepts anonymous requests (REQUIRE_API_KEY off); no API key needed.")
		return nil
	case gateway.AuthUnreachable:
		if !in.interactive {
			return fmt.Errorf("OmniRoute is unreachable at %s and OMNIROUTE_API_KEY is not set; start the server and export OMNIROUTE_API_KEY (see `omniharness doctor`)", cfg.OmniRoute.Endpoint)
		}
		fmt.Fprintf(os.Stderr, "note: OmniRoute is unreachable at %s; you can still provide a key now.\n", cfg.OmniRoute.Endpoint)
	default:
		if !in.interactive {
			return fmt.Errorf("OMNIROUTE_API_KEY is not set and stdin is not a terminal; export OMNIROUTE_API_KEY=sk-… first (see `omniharness doctor`)")
		}
	}

	key, err := promptAPIKey(in.stdin)
	if err != nil {
		return fmt.Errorf("read API key: %w", err)
	}
	if key == "" {
		return fmt.Errorf("no API key provided; set OMNIROUTE_API_KEY=sk-… and re-run")
	}
	rt.Gateway.SetAPIKey(key)

	// Re-validate when the server is reachable; warn (don't block) on rejection
	// so a typo'd key doesn't brick the launch — doctor will surface the state.
	switch d := rt.Gateway.Diagnose(ctx); d.State {
	case gateway.AuthOK:
		fmt.Fprintf(os.Stderr, "authenticated with key_%s\n", last4(key))
	case gateway.AuthRejected:
		fmt.Fprintf(os.Stderr, "warning: OmniRoute rejected the provided key (key_%s); check it and re-run `omniharness doctor`\n", last4(key))
	case gateway.AuthUnreachable:
		fmt.Fprintf(os.Stderr, "key stored for this session (key_%s); will be used once OmniRoute is reachable\n", last4(key))
	}
	return nil
}

// promptAPIKey asks the user to paste their OmniRoute API key. The prompt goes
// to stderr so stdout stays clean for machine-readable output.
func promptAPIKey(stdin io.Reader) (string, error) {
	fmt.Fprint(os.Stderr, "OmniRoute API key not set. Paste your key (sk-…): ")
	line, err := bufio.NewReader(stdin).ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimSpace(line), nil
}
