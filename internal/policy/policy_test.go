package policy

import (
	"context"
	"errors"
	"strings"
	"testing"

	"omniharness/internal/tools"
)

func defaultCfg() Config {
	return Config{
		RiskAction: map[string]string{
			"low": "allow", "medium": "allow", "high": "ask", "critical": "block",
		},
		ShellAllowed:            false,
		GitPushRequiresApproval: true,
	}
}

func TestLowRiskAllowed(t *testing.T) {
	e := NewEngine(defaultCfg(), nil)
	d, reason, err := e.Evaluate(context.Background(), Request{Tool: "read_file", Risk: tools.RiskLow})
	if err != nil {
		t.Fatal(err)
	}
	if d != Allow {
		t.Fatalf("decision %s (%s)", d, reason)
	}
}

func TestHighRiskAsks(t *testing.T) {
	e := NewEngine(defaultCfg(), nil)
	d, reason, _ := e.Evaluate(context.Background(), Request{Tool: "deploy", Risk: tools.RiskHigh})
	if d != Ask {
		t.Fatalf("decision %s (%s)", d, reason)
	}
}

func TestCriticalBlocked(t *testing.T) {
	e := NewEngine(defaultCfg(), nil)
	d, _, _ := e.Evaluate(context.Background(), Request{Tool: "wipe", Risk: tools.RiskCritical})
	if d != Block {
		t.Fatalf("decision %s", d)
	}
}

func TestBlockedTools(t *testing.T) {
	cfg := defaultCfg()
	cfg.BlockedTools = []string{"shell"}
	e := NewEngine(cfg, nil)
	d, reason, _ := e.Evaluate(context.Background(), Request{Tool: "shell", Risk: tools.RiskHigh})
	if d != Block || !contains(reason, "blocked by policy") {
		t.Fatalf("d=%s reason=%q", d, reason)
	}
}

func TestAllowedToolsWhitelist(t *testing.T) {
	cfg := defaultCfg()
	cfg.AllowedTools = []string{"read_file"}
	e := NewEngine(cfg, nil)
	d, _, _ := e.Evaluate(context.Background(), Request{Tool: "write_file", Risk: tools.RiskMedium})
	if d != Block {
		t.Fatal("write_file must be blocked when not whitelisted")
	}
	d, _, _ = e.Evaluate(context.Background(), Request{Tool: "read_file", Risk: tools.RiskLow})
	if d != Allow {
		t.Fatal("read_file should pass whitelist")
	}
}

func TestShellDisabledByDefault(t *testing.T) {
	e := NewEngine(defaultCfg(), nil)
	d, reason, _ := e.Evaluate(context.Background(), Request{Tool: "shell", Risk: tools.RiskHigh})
	if d != Block || !contains(reason, "disabled by policy") {
		t.Fatalf("d=%s reason=%q", d, reason)
	}
}

func TestGitPushRequiresApproval(t *testing.T) {
	e := NewEngine(defaultCfg(), nil)
	d, _, _ := e.Evaluate(context.Background(), Request{Tool: "git", Risk: tools.RiskHigh, Input: map[string]any{"args": []any{"push"}}})
	if d != Ask {
		t.Fatalf("push must ask, got %s", d)
	}
	// Non-push git ops fall back to the risk action.
	d, _, _ = e.Evaluate(context.Background(), Request{Tool: "git", Risk: tools.RiskHigh, Input: map[string]any{"args": []any{"status"}}})
	if d != Ask {
		t.Fatalf("git status should follow risk action, got %s", d)
	}
}

func TestApproverGrantAndDeny(t *testing.T) {
	cfg := defaultCfg()
	cfg.ShellAllowed = true
	var granted bool
	e := NewEngine(cfg, &fakeApprover{fn: func() bool { return granted }})

	granted = true
	d, err := e.EvaluateAndExecute(context.Background(), Request{Tool: "shell", Risk: tools.RiskHigh})
	if err != nil {
		t.Fatal(err)
	}
	if d != Allow {
		t.Fatalf("expected allow after grant, got %s", d)
	}

	granted = false
	d, err = e.EvaluateAndExecute(context.Background(), Request{Tool: "shell", Risk: tools.RiskHigh})
	if err != nil {
		t.Fatal(err)
	}
	if d != Block {
		t.Fatalf("expected block after deny, got %s", d)
	}
}

func TestNoApproverDenies(t *testing.T) {
	cfg := defaultCfg()
	cfg.ShellAllowed = true
	e := NewEngine(cfg, nil)
	d, err := e.EvaluateAndExecute(context.Background(), Request{Tool: "shell", Risk: tools.RiskHigh})
	if err == nil {
		t.Fatal("expected error when no approver")
	}
	if d != Block {
		t.Fatalf("decision %s", d)
	}
}

func TestWorkspaceConfinement(t *testing.T) {
	cfg := defaultCfg()
	cfg.WorkspaceRoot = "/workspace"
	e := NewEngine(cfg, nil)
	d, reason, _ := e.Evaluate(context.Background(), Request{
		Tool: "write_file", Risk: tools.RiskMedium, Input: map[string]any{"path": "/etc/passwd"},
	})
	if d != Block || !contains(reason, "outside the workspace") {
		t.Fatalf("d=%s reason=%q", d, reason)
	}
	d, _, _ = e.Evaluate(context.Background(), Request{
		Tool: "write_file", Risk: tools.RiskMedium, Input: map[string]any{"path": "/workspace/a.txt"},
	})
	if d != Allow {
		t.Fatalf("in-workspace write blocked: %s", d)
	}
}

type fakeApprover struct {
	fn func() bool
}

func (f *fakeApprover) RequestApproval(_ context.Context, _ Request, _ string) (bool, error) {
	return f.fn(), nil
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// A tool that declares a risk class the engine does not know must not slip
// through the gate. The four known classes are always configured, so reaching
// the fallback means the tool mislabelled itself — previously that was treated
// as "allow", which let a tool opt out of approval by naming a risk nobody
// recognised.
func TestUnknownRiskClassRequiresApproval(t *testing.T) {
	e := NewEngine(Config{
		RiskAction: map[string]string{
			"low": "allow", "medium": "allow", "high": "ask", "critical": "block",
		},
	}, nil)

	for _, risk := range []tools.Risk{"", "unspecified", "LOW", "extreme"} {
		decision, reason, err := e.Evaluate(context.Background(), Request{Tool: "some_tool", Risk: risk})
		if err != nil {
			t.Fatal(err)
		}
		if decision == Allow {
			t.Errorf("risk %q was allowed outright (%s); an unknown class must not bypass the gate", risk, reason)
		}
	}

	// The known classes keep behaving exactly as configured.
	if d, _, _ := e.Evaluate(context.Background(), Request{Tool: "read_file", Risk: tools.RiskLow}); d != Allow {
		t.Errorf("low risk = %v, want Allow", d)
	}
	if d, _, _ := e.Evaluate(context.Background(), Request{Tool: "write_file", Risk: tools.RiskHigh}); d != Ask {
		t.Errorf("high risk = %v, want Ask", d)
	}
}

// Setting critical = "ask" does not make critical tools promptable: they are
// refused whether or not an approver is connected. This is deliberate and it
// fails safe, but it means a config option reads as if it does something it
// does not, so pin it rather than leave it to be rediscovered.
func TestCriticalCannotBeDowngradedToAPrompt(t *testing.T) {
	cfg := defaultCfg()
	cfg.RiskAction["critical"] = "ask"
	cfg.ShellAllowed = true

	asked := false
	e := NewEngine(cfg, &fakeApprover{fn: func() bool { asked = true; return true }})

	d, reason, err := e.Evaluate(context.Background(), Request{Tool: "shell", Risk: tools.RiskCritical})
	if err != nil {
		t.Fatal(err)
	}
	if d != Block {
		t.Fatalf("critical with ask = %s, want block", d)
	}
	if strings.Contains(reason, "none configured") {
		t.Errorf("reason %q blames a missing approver, but one is connected", reason)
	}

	got, err := e.EvaluateAndExecute(context.Background(), Request{Tool: "shell", Risk: tools.RiskCritical})
	if err != nil {
		t.Fatal(err)
	}
	if got != Block {
		t.Fatalf("EvaluateAndExecute = %s, want block", got)
	}
	if asked {
		t.Error("the approver must never be consulted for a critical tool")
	}
}

// An approver that fails — a closed TUI, a cancelled context — must block.
// Treating the error as anything else would run the tool nobody approved.
func TestApproverErrorBlocks(t *testing.T) {
	cfg := defaultCfg()
	cfg.ShellAllowed = true
	e := NewEngine(cfg, ApproverFunc(func(context.Context, Request, string) (bool, error) {
		return true, errors.New("prompt surface is gone")
	}))

	d, err := e.EvaluateAndExecute(context.Background(), Request{Tool: "shell", Risk: tools.RiskHigh})
	if err == nil {
		t.Fatal("an approver error must be reported")
	}
	if d != Block {
		t.Fatalf("decision = %s, want block; a granted-but-errored approval is not a grant", d)
	}
}

// The agent loop calls EvaluateAndExecute for every tool, so a decision that
// never needed a human must not reach the approver at all.
func TestAllowAndBlockNeverConsultTheApprover(t *testing.T) {
	cfg := defaultCfg()
	consulted := 0
	e := NewEngine(cfg, ApproverFunc(func(context.Context, Request, string) (bool, error) {
		consulted++
		return true, nil
	}))

	for _, tc := range []struct {
		name string
		req  Request
		want Decision
	}{
		{"low risk", Request{Tool: "read_file", Risk: tools.RiskLow}, Allow},
		{"shell off", Request{Tool: "shell", Risk: tools.RiskHigh}, Block},
		{"no tool name", Request{Risk: tools.RiskLow}, Block},
	} {
		got, err := e.EvaluateAndExecute(context.Background(), tc.req)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got != tc.want {
			t.Errorf("%s: decision = %s, want %s", tc.name, got, tc.want)
		}
	}
	if consulted != 0 {
		t.Errorf("the approver was consulted %d times for decisions that needed no human", consulted)
	}
}

func TestSetApproverReplacesTheGate(t *testing.T) {
	cfg := defaultCfg()
	cfg.ShellAllowed = true
	e := NewEngine(cfg, nil)

	// Before an approver exists, an Ask is a block with an explanation
	// rather than a silent pass.
	if _, err := e.EvaluateAndExecute(context.Background(), Request{Tool: "shell", Risk: tools.RiskHigh}); err == nil {
		t.Fatal("no approver must be an error, not an allow")
	}

	e.SetApprover(ApproverFunc(func(context.Context, Request, string) (bool, error) { return true, nil }))
	d, err := e.EvaluateAndExecute(context.Background(), Request{Tool: "shell", Risk: tools.RiskHigh})
	if err != nil || d != Allow {
		t.Fatalf("after SetApprover: %s, %v; want allow", d, err)
	}

	// Replacing it again takes effect immediately: a TUI that closes must not
	// leave its old approver answering.
	e.SetApprover(ApproverFunc(func(context.Context, Request, string) (bool, error) { return false, nil }))
	if d, err := e.EvaluateAndExecute(context.Background(), Request{Tool: "shell", Risk: tools.RiskHigh}); err != nil || d != Block {
		t.Fatalf("after replacing the approver: %s, %v; want block", d, err)
	}
}

func TestDecisionString(t *testing.T) {
	// These strings are written to the session store and printed in the TUI.
	for _, tc := range []struct {
		d    Decision
		want string
	}{{Allow, "allow"}, {Ask, "ask"}, {Block, "block"}, {Decision(99), "unknown"}} {
		if got := tc.d.String(); got != tc.want {
			t.Errorf("Decision(%d).String() = %q, want %q", tc.d, got, tc.want)
		}
	}
}

// The reason is not decoration: it is what the human reads in the approval
// prompt and what lands in the audit trail, so every refusal must say which
// rule refused.
func TestEveryRefusalExplainsItself(t *testing.T) {
	cfg := defaultCfg()
	cfg.BlockedTools = []string{"delete_everything"}
	cfg.WorkspaceRoot = "/work"
	cfg.GitPushRequiresApproval = true
	e := NewEngine(cfg, nil)

	for _, tc := range []struct {
		name string
		req  Request
		want string
	}{
		{"blocked tool", Request{Tool: "delete_everything", Risk: tools.RiskLow}, "blocked by policy"},
		{"shell off", Request{Tool: "shell", Risk: tools.RiskHigh}, "shell_allowed"},
		{"outside workspace", Request{Tool: "write_file", Risk: tools.RiskHigh, Input: map[string]any{"path": "/etc/passwd"}}, "outside the workspace root"},
		{"git push", Request{Tool: "git", Risk: tools.RiskLow, Input: map[string]any{"args": []any{"push"}}}, "requires explicit approval"},
		{"unknown risk", Request{Tool: "read_file", Risk: tools.Risk("spicy")}, "unrecognised risk class"},
		{"no tool name", Request{Risk: tools.RiskLow}, "missing tool name"},
	} {
		d, reason, err := e.Evaluate(context.Background(), tc.req)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if d == Allow {
			t.Errorf("%s: decision = allow, want a refusal or a prompt", tc.name)
		}
		if !strings.Contains(reason, tc.want) {
			t.Errorf("%s: reason = %q, want it to mention %q", tc.name, reason, tc.want)
		}
	}
}
