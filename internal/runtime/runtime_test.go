package runtime

import (
	"context"
	"strings"
	"testing"
	"time"

	"omniharness/internal/config"
	"omniharness/internal/event"
	"omniharness/internal/gateway"
	"omniharness/internal/task"
	"omniharness/internal/testutil"
)

func testRuntime(t *testing.T, fake *testutil.FakeOmniRoute, dir string) *Runtime {
	t.Helper()
	cfg := config.Default()
	cfg.Persistence.Dir = dir
	rt, err := New(cfg, Options{
		Gateway:                fake.Client(),
		DisablePersistenceSink: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(rt.Close)
	return rt
}

func TestRuntimeRunsTaskAndPersists(t *testing.T) {
	fake := testutil.NewFakeOmniRoute(t, testutil.FakeStep{Content: "deliverable"})
	rt := testRuntime(t, fake, t.TempDir())

	ss, err := rt.NewSession("", "test task")
	if err != nil {
		t.Fatal(err)
	}
	tsk, err := rt.RunTask(context.Background(), ss.ID, "Fix the typo in README.md.", RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if tsk.Status != task.StatusCompleted {
		t.Fatalf("status = %s: %s", tsk.Status, tsk.Error)
	}

	// Durable: reload from the store.
	got, err := rt.Store.GetTask(tsk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != task.StatusCompleted || got.Strategy == "" {
		t.Fatalf("reloaded task %+v", got)
	}

	// Durability invariant: by the time RunTask returns, the terminal event
	// must already be durable — no polling, no sleep. The bounded flush in
	// RunTask guarantees task.completed is on disk before the caller sees the
	// result.
	evs, err := rt.SessionEvents(ss.ID, 100)
	if err != nil {
		t.Fatal(err)
	}
	types := map[event.Type]bool{}
	for _, e := range evs {
		types[e.Type] = true
	}
	for _, want := range []event.Type{event.TaskCreated, event.TaskAnalyzed, event.StrategySelected, event.AgentCreated, event.TaskCompleted} {
		if !types[want] {
			t.Fatalf("missing persisted event %s (have %v)", want, types)
		}
	}

	// Model calls recorded.
	calls, err := rt.Store.ModelCalls(ss.ID)
	if err != nil || len(calls) == 0 {
		t.Fatalf("model calls %v err %v", calls, err)
	}
}

func TestRuntimeSessionsListed(t *testing.T) {
	fake := testutil.NewFakeOmniRoute(t, testutil.FakeStep{Content: "x"})
	rt := testRuntime(t, fake, t.TempDir())
	if _, err := rt.NewSession("", "alpha"); err != nil {
		t.Fatal(err)
	}
	if _, err := rt.NewSession("", "beta"); err != nil {
		t.Fatal(err)
	}
	sessions, err := rt.ListSessions(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 2 {
		t.Fatalf("sessions = %d", len(sessions))
	}
}

func TestRuntimePing(t *testing.T) {
	fake := testutil.NewFakeOmniRoute(t)
	rt := testRuntime(t, fake, t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := rt.Ping(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}
}

func TestRuntimeCancellation(t *testing.T) {
	fake := testutil.NewFakeOmniRoute(t,
		testutil.FakeStep{ToolCalls: []gateway.ToolCall{testutil.ToolCall("c1", "read_file", `{"path":"x"}`)}, Delay: 300 * time.Millisecond},
	)
	rt := testRuntime(t, fake, t.TempDir())
	ss, _ := rt.NewSession("", "cancel me")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_, _ = rt.RunTask(ctx, ss.ID, "Add a feature to the codebase.", RunOptions{})
		close(done)
	}()
	time.Sleep(400 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("runtime did not stop on cancel")
	}
}

func TestRuntimeBudgetsRespected(t *testing.T) {
	fake := testutil.NewFakeOmniRoute(t, testutil.FakeStep{Content: "x"})
	rt := testRuntime(t, fake, t.TempDir())
	ss, _ := rt.NewSession("", "budgeted")
	tsk, err := rt.RunTask(context.Background(), ss.ID, "do something",
		RunOptions{Deadline: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if tsk.Status != task.StatusCompleted {
		t.Fatalf("status %s: %s", tsk.Status, tsk.Error)
	}
	_ = strings.TrimSpace
}
