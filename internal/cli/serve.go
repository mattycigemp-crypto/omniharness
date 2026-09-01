package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"omniharness/internal/gateway"
	"omniharness/internal/runtime"
	"omniharness/internal/telemetry"
	"omniharness/internal/version"
)

func newServeCmd() *cobra.Command {
	var (
		port int
	)
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run a local headless HTTP API",
		Long: `Starts a loopback-only HTTP API for programmatic task submission and
monitoring.

Requests must address the loopback interface by name and carry no Origin
header, so a web page cannot drive this API through DNS rebinding. POST
/v1/tasks runs an agent with tool access, so treat the port as trusted: any
process on this machine can reach it.

Endpoints:
  GET  /health             liveness + OmniRoute reachability
  POST /v1/tasks           run a task {prompt, sessionId?}
  GET  /v1/sessions        list sessions
  GET  /v1/sessions/{id}   session detail with metrics`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			rt, err := newRuntime(cmd.Context())
			if err != nil {
				return err
			}
			defer rt.Close()
			installApprover(rt, rootOpts.Yes)
			cfg, _ := loadConfig()
			loadMCPServersFromConfig(cmd.Context(), rt, cfg)

			mux := http.NewServeMux()
			mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
				diag := rt.Gateway.Diagnose(r.Context())
				writeJSON(w, http.StatusOK, map[string]any{
					"ok":              true,
					"version":         version.String(),
					"omniroute":       diag.State == gateway.AuthOK || diag.State == gateway.AuthNotRequired,
					"authState":       string(diag.State),
					"omnirouteDetail": diag.Detail,
				})
			})
			mux.HandleFunc("/v1/tasks", func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost {
					http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
					return
				}
				var req struct {
					Prompt     string `json:"prompt"`
					SessionID  string `json:"sessionId,omitempty"`
					ApproveAll bool   `json:"approveAll,omitempty"`
				}
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
					return
				}
				if strings.TrimSpace(req.Prompt) == "" {
					http.Error(w, "prompt is required", http.StatusBadRequest)
					return
				}
				sessionID := req.SessionID
				if sessionID == "" {
					ss, err := rt.NewSession("", truncate(req.Prompt, 60))
					if err != nil {
						http.Error(w, err.Error(), http.StatusInternalServerError)
						return
					}
					sessionID = ss.ID
				}
				ctx, cancel := context.WithTimeout(r.Context(), 30*time.Minute)
				defer cancel()
				tsk, err := rt.RunTask(ctx, sessionID, req.Prompt, runtime.RunOptions{
					ApproveAll: req.ApproveAll,
				})
				if err != nil {
					writeJSON(w, http.StatusOK, map[string]any{
						"sessionId": sessionID,
						"task":      tsk,
						"error":     err.Error(),
					})
					return
				}
				writeJSON(w, http.StatusOK, map[string]any{"sessionId": sessionID, "task": tsk})
			})
			mux.HandleFunc("/v1/sessions/", func(w http.ResponseWriter, r *http.Request) {
				id := strings.TrimPrefix(r.URL.Path, "/v1/sessions/")
				ss, err := rt.Store.GetSession(id)
				if err != nil {
					http.Error(w, "session not found", http.StatusNotFound)
					return
				}
				tasks, _ := rt.Store.TasksBySession(id)
				m, _ := telemetry.ForSession(rt.Store, id)
				writeJSON(w, http.StatusOK, map[string]any{"session": ss, "tasks": tasks, "metrics": m})
			})
			mux.HandleFunc("/v1/sessions", func(w http.ResponseWriter, r *http.Request) {
				sessions, err := rt.ListSessions(50)
				if err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
					return
				}
				writeJSON(w, http.StatusOK, map[string]any{"sessions": sessions})
			})

			addr := fmt.Sprintf("127.0.0.1:%d", port)
			fmt.Printf("omniharness serve listening on http://%s\n", addr)
			srv := &http.Server{
				Addr:    addr,
				Handler: guardLoopback(mux),
				// A connection that never finishes sending its headers would
				// otherwise occupy the server indefinitely.
				ReadHeaderTimeout: 10 * time.Second,
				IdleTimeout:       120 * time.Second,
				// No WriteTimeout: a task legitimately runs for minutes, and the
				// handler already bounds it at 30.
			}
			go func() {
				<-cmd.Context().Done()
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				srv.Shutdown(ctx)
			}()
			if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				return err
			}
			return nil
		},
	}
	cmd.Flags().IntVar(&port, "port", 20140, "loopback port")
	return cmd
}

// guardLoopback rejects requests that a local client would never send.
//
// Binding to 127.0.0.1 keeps the API off the network, but it does not keep a
// browser out. Under DNS rebinding, a page the user is merely visiting can
// resolve its own hostname to 127.0.0.1 and post here; the request arrives on
// the loopback socket like any other. That matters more than usual for this
// API, because POST /v1/tasks runs an agent with full tool access and accepts
// approveAll, which waives the approval gate outright.
//
// Two checks close it, and neither inconveniences a real client:
//
//   - The Host header must name the loopback interface. A rebound request
//     carries the attacker's hostname, because that is what the browser
//     resolved.
//   - No Origin header. Browsers attach one to every cross-origin request;
//     curl, the Go client and the harness itself do not send one at all.
func guardLoopback(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); origin != "" {
			http.Error(w, "cross-origin requests are not accepted", http.StatusForbidden)
			return
		}
		if !isLoopbackHost(r.Host) {
			http.Error(w, "host must be the loopback interface", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// isLoopbackHost reports whether a Host header names the local machine.
// The port is optional, and a bare IPv6 literal may arrive without brackets.
func isLoopbackHost(host string) bool {
	if host == "" {
		return false
	}
	name := host
	if h, _, err := net.SplitHostPort(host); err == nil {
		name = h
	}
	name = strings.TrimSuffix(strings.TrimPrefix(name, "["), "]")
	if strings.EqualFold(name, "localhost") {
		return true
	}
	if ip := net.ParseIP(name); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
