package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"omniharness/internal/session"
	"omniharness/internal/task"
	"omniharness/internal/telemetry"
)

func statsStore(t *testing.T) *session.Store {
	t.Helper()
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func seedRuns(t *testing.T, store *session.Store) {
	t.Helper()
	if err := store.CreateSession(&session.Session{ID: "s1"}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	// Latencies that do not divide evenly: the average is the figure that
	// used to make the whole model breakdown fail to scan.
	calls := []session.ModelCall{
		{ID: "m1", SessionID: "s1", Model: "openai/gpt-5", TokensIn: 100, TokensOut: 20, CostUSD: 0.10, LatencyMS: 200, Status: "ok"},
		{ID: "m2", SessionID: "s1", Model: "openai/gpt-5", TokensIn: 50, TokensOut: 10, CostUSD: 0.05, LatencyMS: 401, Status: "failed"},
		{ID: "m3", SessionID: "s1", Model: "anthropic/claude", TokensIn: 10, TokensOut: 1, CostUSD: 0.90, LatencyMS: 100, Status: "ok"},
	}
	for i := range calls {
		if err := store.RecordModelCall(&calls[i]); err != nil {
			t.Fatalf("record model call: %v", err)
		}
	}
	tools := []session.ToolCall{
		{ID: "t1", SessionID: "s1", Tool: "read_file", Status: "completed"},
		{ID: "t2", SessionID: "s1", Tool: "read_file", Status: "failed"},
		{ID: "t3", SessionID: "s1", Tool: "shell", Status: "denied"},
	}
	for i := range tools {
		if err := store.RecordToolCall(&tools[i]); err != nil {
			t.Fatalf("record tool call: %v", err)
		}
	}
	if err := store.CreateTask(&task.Task{ID: "task-1", SessionID: "s1", Status: task.StatusCompleted, Repairs: 1}); err != nil {
		t.Fatalf("create task: %v", err)
	}
}

func TestCollectStatsReadsAllThreeViews(t *testing.T) {
	store := statsStore(t)
	seedRuns(t, store)

	r, err := collectStats(store)
	if err != nil {
		t.Fatalf("collectStats: %v", err)
	}
	if r.Totals.Sessions != 1 || r.Totals.ModelCalls != 3 || r.Totals.ToolCalls != 3 {
		t.Errorf("totals = %+v", r.Totals)
	}
	if len(r.Models) != 2 {
		t.Fatalf("got %d models, want 2", len(r.Models))
	}
	if len(r.Tools) != 2 {
		t.Fatalf("got %d tools, want 2", len(r.Tools))
	}
	// Costliest first, so the row worth acting on leads.
	if r.Models[0].Model != "anthropic/claude" {
		t.Errorf("first row = %q, want the costliest model", r.Models[0].Model)
	}
	// 601/2 = 300.5, which is the case that used to fail to scan at all.
	var gpt telemetry.ModelStats
	for _, m := range r.Models {
		if m.Model == "openai/gpt-5" {
			gpt = m
		}
	}
	if gpt.AvgMS != 300 {
		t.Errorf("gpt-5 avg = %d, want 300 from a fractional average", gpt.AvgMS)
	}
	if gpt.Failed != 1 {
		t.Errorf("gpt-5 failed = %d, want 1", gpt.Failed)
	}
}

func TestCollectStatsReportsFailureRatherThanZeroes(t *testing.T) {
	store := statsStore(t)
	seedRuns(t, store)
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// A broken store must not render as a plausible-looking empty report.
	if _, err := collectStats(store); err == nil {
		t.Fatal("collectStats on a closed store returned no error")
	}
}

func TestPrintStatsRendersEveryBreakdown(t *testing.T) {
	store := statsStore(t)
	seedRuns(t, store)
	r, err := collectStats(store)
	if err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	printStats(&buf, r)
	out := buf.String()

	for _, want := range []string{
		"1 session, 1 task (1 completed, 0 failed), 1 repair",
		"BY MODEL", "openai/gpt-5", "anthropic/claude",
		"BY TOOL", "read_file", "shell",
		"$1.0500", // the totals line
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}

	// A denied call is the number worth surfacing: it says the policy gate
	// turned something away.
	toolLine := lineContaining(t, out, "shell")
	if !strings.HasSuffix(strings.TrimSpace(toolLine), "1") {
		t.Errorf("shell row = %q, want a denied count of 1", toolLine)
	}
}

func TestPrintStatsOnAnEmptyStore(t *testing.T) {
	store := statsStore(t)
	r, err := collectStats(store)
	if err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	printStats(&buf, r)
	got := strings.TrimSpace(buf.String())
	if got != "no runs recorded yet" {
		t.Errorf("empty store printed %q, want a plain line saying nothing has run", got)
	}
	if strings.Contains(got, "BY MODEL") {
		t.Error("empty headers must not be printed")
	}
}

func TestStatsJSONCarriesTheNumbersWithoutParsingColumns(t *testing.T) {
	store := statsStore(t)
	seedRuns(t, store)
	r, err := collectStats(store)
	if err != nil {
		t.Fatal(err)
	}

	b, err := jsonMarshalIndent(r)
	if err != nil {
		t.Fatal(err)
	}
	var back struct {
		Totals telemetry.GlobalMetrics `json:"totals"`
		Models []telemetry.ModelStats  `json:"models"`
		Tools  []telemetry.ToolStats   `json:"tools"`
	}
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("the --json output must round-trip: %v", err)
	}
	if back.Totals.ModelCalls != 3 || len(back.Models) != 2 || len(back.Tools) != 2 {
		t.Errorf("round-tripped report = %+v", back)
	}
}

func TestHumanMS(t *testing.T) {
	for _, tc := range []struct {
		ms   int64
		want string
	}{
		{0, "0ms"}, {999, "999ms"}, {1000, "1.0s"}, {1500, "1.5s"},
		{59_999, "60.0s"}, {60_000, "1m0s"}, {125_000, "2m5s"},
	} {
		if got := humanMS(tc.ms); got != tc.want {
			t.Errorf("humanMS(%d) = %q, want %q", tc.ms, got, tc.want)
		}
	}
}

func lineContaining(t *testing.T, out, want string) string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, want) && !strings.Contains(line, "TOOL") {
			return line
		}
	}
	t.Fatalf("no line containing %q in:\n%s", want, out)
	return ""
}
