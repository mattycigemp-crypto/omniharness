package telemetry

import (
	"testing"
	"time"

	"omniharness/internal/session"
	"omniharness/internal/task"
)

// These are the status strings internal/agent actually writes when it records
// a tool call. The metrics below count them by name in SQL, so if the agent
// ever renames one the counter silently reads zero — a number that is wrong
// rather than missing. Keeping them here means that drift breaks a test.
const (
	statusCompleted = "completed"
	statusFailed    = "failed"
	statusDenied    = "denied"
)

// fixture builds a store holding two sessions, so every per-session metric
// has a neighbouring session it must not count.
func fixture(t *testing.T) *session.Store {
	t.Helper()
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	for _, id := range []string{"sess-a", "sess-b"} {
		if err := store.CreateSession(&session.Session{ID: id, CreatedAt: time.Now().UTC()}); err != nil {
			t.Fatalf("create session %s: %v", id, err)
		}
	}

	modelCalls := []session.ModelCall{
		{ID: "m1", SessionID: "sess-a", Model: "openai/gpt-5", TokensIn: 100, TokensOut: 20, CostUSD: 0.10, LatencyMS: 200, Status: "ok"},
		{ID: "m2", SessionID: "sess-a", Model: "openai/gpt-5", TokensIn: 50, TokensOut: 10, CostUSD: 0.05, LatencyMS: 400, Status: statusFailed},
		{ID: "m3", SessionID: "sess-a", Model: "anthropic/claude", TokensIn: 10, TokensOut: 1, CostUSD: 0.90, LatencyMS: 100, Status: "ok"},
		{ID: "m4", SessionID: "sess-b", Model: "openai/gpt-5", TokensIn: 7000, TokensOut: 7000, CostUSD: 7.00, LatencyMS: 7000, Status: "ok"},
	}
	for i := range modelCalls {
		if err := store.RecordModelCall(&modelCalls[i]); err != nil {
			t.Fatalf("record model call: %v", err)
		}
	}

	toolCalls := []session.ToolCall{
		{ID: "t1", SessionID: "sess-a", Tool: "read_file", Status: statusCompleted},
		{ID: "t2", SessionID: "sess-a", Tool: "read_file", Status: statusCompleted},
		{ID: "t3", SessionID: "sess-a", Tool: "read_file", Status: statusFailed},
		{ID: "t4", SessionID: "sess-a", Tool: "shell", Status: statusDenied},
		{ID: "t5", SessionID: "sess-b", Tool: "shell", Status: statusDenied},
	}
	for i := range toolCalls {
		if err := store.RecordToolCall(&toolCalls[i]); err != nil {
			t.Fatalf("record tool call: %v", err)
		}
	}

	evals := []session.Evaluation{
		{ID: "e1", SessionID: "sess-a", Evaluator: "build", Outcome: "pass"},
		{ID: "e2", SessionID: "sess-a", Evaluator: "test", Outcome: "fail"},
		{ID: "e3", SessionID: "sess-b", Evaluator: "build", Outcome: "pass"},
	}
	for i := range evals {
		if err := store.RecordEvaluation(&evals[i]); err != nil {
			t.Fatalf("record evaluation: %v", err)
		}
	}

	tasks := []*task.Task{
		{ID: "task-1", SessionID: "sess-a", Status: task.StatusCompleted, Repairs: 2},
		{ID: "task-2", SessionID: "sess-a", Status: task.StatusFailed, Repairs: 1},
		{ID: "task-3", SessionID: "sess-b", Status: task.StatusCompleted},
	}
	for _, tsk := range tasks {
		if err := store.CreateTask(tsk); err != nil {
			t.Fatalf("create task: %v", err)
		}
	}
	return store
}

func TestForSessionCountsOnlyItsOwnSession(t *testing.T) {
	store := fixture(t)

	m, err := ForSession(store, "sess-a")
	if err != nil {
		t.Fatalf("ForSession: %v", err)
	}
	if m.SessionID != "sess-a" {
		t.Errorf("SessionID = %q", m.SessionID)
	}
	// sess-b's single enormous model call would swamp every one of these if
	// the scoping were wrong, which is the point of its size.
	if m.ModelCalls != 3 {
		t.Errorf("ModelCalls = %d, want 3", m.ModelCalls)
	}
	if m.TokensIn != 160 || m.TokensOut != 31 {
		t.Errorf("tokens = %d/%d, want 160/31", m.TokensIn, m.TokensOut)
	}
	if m.CostUSD < 1.0499 || m.CostUSD > 1.0501 {
		t.Errorf("CostUSD = %v, want 1.05", m.CostUSD)
	}
	if m.LatencyMS != 700 {
		t.Errorf("LatencyMS = %d, want 700", m.LatencyMS)
	}
	if m.FailedCalls != 1 {
		t.Errorf("FailedCalls = %d, want 1", m.FailedCalls)
	}
	if m.ToolCalls != 4 {
		t.Errorf("ToolCalls = %d, want 4", m.ToolCalls)
	}
	if m.Evaluations != 2 {
		t.Errorf("Evaluations = %d, want 2", m.Evaluations)
	}
	if m.StartedAt.IsZero() {
		t.Error("StartedAt was never read back from the session row")
	}
}

func TestForSessionOfAnUnknownSession(t *testing.T) {
	store := fixture(t)

	// A session id nobody recorded against must read as zeroes, not as an
	// error and not as the whole store.
	m, err := ForSession(store, "sess-missing")
	if err != nil {
		t.Fatalf("ForSession of an unknown session = %v, want no error", err)
	}
	if m.ModelCalls != 0 || m.ToolCalls != 0 || m.Evaluations != 0 || m.CostUSD != 0 {
		t.Errorf("unknown session reported activity: %+v", m)
	}
	if !m.StartedAt.IsZero() {
		t.Errorf("StartedAt = %v, want zero", m.StartedAt)
	}
}

func TestGlobalAggregatesEverySession(t *testing.T) {
	store := fixture(t)

	g, err := Global(store)
	if err != nil {
		t.Fatalf("Global: %v", err)
	}
	if g.Sessions != 2 {
		t.Errorf("Sessions = %d, want 2", g.Sessions)
	}
	if g.Tasks != 3 || g.Completed != 2 || g.Failed != 1 {
		t.Errorf("tasks = %d (%d completed, %d failed), want 3 (2, 1)", g.Tasks, g.Completed, g.Failed)
	}
	if g.ModelCalls != 4 || g.ToolCalls != 5 {
		t.Errorf("calls = %d model / %d tool, want 4/5", g.ModelCalls, g.ToolCalls)
	}
	if g.Tokens != 14191 { // 160 + 31 in sess-a, 14000 in sess-b
		t.Errorf("Tokens = %d, want 14191 (in and out summed)", g.Tokens)
	}
	if g.CostUSD < 8.0499 || g.CostUSD > 8.0501 {
		t.Errorf("CostUSD = %v, want 8.05", g.CostUSD)
	}
	if g.TotalLatency != 7700 {
		t.Errorf("TotalLatency = %d, want 7700", g.TotalLatency)
	}
	if g.Repairs != 3 {
		t.Errorf("Repairs = %d, want 3", g.Repairs)
	}
}

func TestGlobalOnAnEmptyStore(t *testing.T) {
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	// Every aggregate is COALESCEd, so a fresh install must report zeroes
	// rather than failing to scan a NULL.
	g, err := Global(store)
	if err != nil {
		t.Fatalf("Global on an empty store = %v, want no error", err)
	}
	if g != (GlobalMetrics{}) {
		t.Errorf("empty store reported %+v, want all zeroes", g)
	}

	if models, err := ByModel(store); err != nil || len(models) != 0 {
		t.Errorf("ByModel = %v, %v; want empty and no error", models, err)
	}
	if tools, err := ByTool(store); err != nil || len(tools) != 0 {
		t.Errorf("ByTool = %v, %v; want empty and no error", tools, err)
	}
}

func TestByModelGroupsAndOrdersByCost(t *testing.T) {
	store := fixture(t)

	stats, err := ByModel(store)
	if err != nil {
		t.Fatalf("ByModel: %v", err)
	}
	if len(stats) != 2 {
		t.Fatalf("got %d models, want 2", len(stats))
	}
	// Costliest first: that is what the breakdown is read for.
	if stats[0].Model != "openai/gpt-5" || stats[1].Model != "anthropic/claude" {
		t.Fatalf("order = %q, %q; want the costliest model first", stats[0].Model, stats[1].Model)
	}

	gpt := stats[0]
	if gpt.Calls != 3 {
		t.Errorf("Calls = %d, want 3 across both sessions", gpt.Calls)
	}
	if gpt.Failed != 1 {
		t.Errorf("Failed = %d, want 1", gpt.Failed)
	}
	if gpt.TokensIn != 7150 || gpt.TokensOut != 7030 {
		t.Errorf("tokens = %d/%d, want 7150/7030", gpt.TokensIn, gpt.TokensOut)
	}
	if gpt.CostUSD < 7.1499 || gpt.CostUSD > 7.1501 {
		t.Errorf("CostUSD = %v, want 7.15", gpt.CostUSD)
	}
	// (200 + 400 + 7000) / 3
	if gpt.AvgMS != 2533 {
		t.Errorf("AvgMS = %d, want 2533", gpt.AvgMS)
	}
}

func TestByToolCountsDeniedSeparately(t *testing.T) {
	store := fixture(t)

	stats, err := ByTool(store)
	if err != nil {
		t.Fatalf("ByTool: %v", err)
	}
	if len(stats) != 2 {
		t.Fatalf("got %d tools, want 2", len(stats))
	}
	if stats[0].Tool != "read_file" {
		t.Fatalf("order = %q first, want the most-called tool", stats[0].Tool)
	}

	read := stats[0]
	if read.Calls != 3 || read.Failed != 1 || read.Denied != 0 {
		t.Errorf("read_file = %d calls, %d failed, %d denied; want 3/1/0", read.Calls, read.Failed, read.Denied)
	}

	// A denied call is the one number here that matters for review: it says
	// the policy gate turned something away. It is counted by matching the
	// status string the agent writes, so this is the assertion that breaks
	// if either side is renamed.
	shell := stats[1]
	if shell.Denied != 2 {
		t.Errorf("shell denied = %d, want 2", shell.Denied)
	}
	if shell.Failed != 0 {
		t.Errorf("a denied call must not also count as failed, got %d", shell.Failed)
	}
}

// Callers that render metrics tend to write `m, _ := ForSession(...)`, so a
// broken store shows up as a confident row of zeroes. The least these
// functions can do is report the failure rather than return zeroes and nil.
func TestMetricsReportStoreFailures(t *testing.T) {
	store := fixture(t)
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	if _, err := ForSession(store, "sess-a"); err == nil {
		t.Error("ForSession on a closed store returned no error")
	}
	if _, err := Global(store); err == nil {
		t.Error("Global on a closed store returned no error")
	}
	if _, err := ByModel(store); err == nil {
		t.Error("ByModel on a closed store returned no error")
	}
	if _, err := ByTool(store); err == nil {
		t.Error("ByTool on a closed store returned no error")
	}
}
