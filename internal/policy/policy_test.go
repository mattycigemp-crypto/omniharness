package policy

import (
	"context"
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
