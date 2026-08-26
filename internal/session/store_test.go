package session

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"omniharness/internal/event"
	"omniharness/internal/id"
	"omniharness/internal/task"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestSessionRoundTrip(t *testing.T) {
	s := openTest(t)
	ss := &Session{ID: id.New(), Title: "test", CWD: "/tmp", Status: "active"}
	if err := s.CreateSession(ss); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetSession(ss.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "test" || got.CWD != "/tmp" {
		t.Fatalf("got %+v", got)
	}
	if err := s.EndSession(ss.ID, "done"); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetSession(ss.ID)
	if got.Status != "ended" || got.Summary != "done" {
		t.Fatalf("after end: %+v", got)
	}
}

func TestListSessionsNewestFirst(t *testing.T) {
	s := openTest(t)
	for i := 0; i < 3; i++ {
		ss := &Session{Title: string(rune('a' + i)), CWD: "/tmp"}
		if err := s.CreateSession(ss); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Millisecond)
	}
	list, err := s.ListSessions(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 {
		t.Fatalf("len = %d", len(list))
	}
	if list[0].Title != "c" {
		t.Fatalf("newest first expected c, got %s", list[0].Title)
	}
}

func TestTaskRoundTrip(t *testing.T) {
	s := openTest(t)
	ss := &Session{ID: id.New()}
	_ = s.CreateSession(ss)
	tsk := &task.Task{
		ID:        id.New(),
		SessionID: ss.ID,
		Spec:      task.Spec{Prompt: "do the thing", CWD: "/tmp"},
		Status:    task.StatusRunning,
	}
	if err := s.CreateTask(tsk); err != nil {
		t.Fatal(err)
	}
	tsk.Status = task.StatusCompleted
	tsk.Result = &task.Result{Summary: "done", Artifacts: []string{"a.txt"}}
	if err := s.UpdateTask(tsk); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetTask(tsk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != task.StatusCompleted || got.Result.Summary != "done" {
		t.Fatalf("got %+v", got)
	}
	if got.Result.Artifacts[0] != "a.txt" {
		t.Fatalf("artifacts %v", got.Result.Artifacts)
	}
}

func TestEventPersistenceAndReplay(t *testing.T) {
	s := openTest(t)
	sessionID := id.New()
	e := event.New(&event.TaskCreatedData{Prompt: "hello"})
	e.SessionID = sessionID
	if err := s.AppendEvent(sessionID, e); err != nil {
		t.Fatal(err)
	}
	e2 := event.New(&event.StrategySelectedData{Strategy: "direct", Reason: "trivial"})
	e2.SessionID = sessionID
	if err := s.AppendEvent(sessionID, e2); err != nil {
		t.Fatal(err)
	}
	evs, err := s.Events(sessionID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 2 {
		t.Fatalf("len = %d", len(evs))
	}
	if evs[0].Type != event.TaskCreated {
		t.Fatalf("first = %s", evs[0].Type)
	}
	p, err := event.Decode(evs[1])
	if err != nil {
		t.Fatal(err)
	}
	sd, ok := p.(*event.StrategySelectedData)
	if !ok || sd.Strategy != "direct" {
		t.Fatalf("decoded %T %+v", p, p)
	}
}

func TestModelCallRecording(t *testing.T) {
	s := openTest(t)
	sessionID := id.New()
	mc := &ModelCall{
		SessionID: sessionID, TaskID: "t1", AgentID: "a1", Provider: "cursor",
		Model: "claude-x", TokensIn: 100, TokensOut: 50, CostUSD: 0.01, LatencyMS: 500, Status: "ok",
	}
	if err := s.RecordModelCall(mc); err != nil {
		t.Fatal(err)
	}
	calls, err := s.ModelCalls(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0].Model != "claude-x" || calls[0].TokensIn != 100 {
		t.Fatalf("calls %+v", calls)
	}
}

func TestToolCallAndEvaluationRecording(t *testing.T) {
	s := openTest(t)
	sessionID := id.New()
	tc := &ToolCall{SessionID: sessionID, TaskID: "t1", Tool: "shell", Status: "completed", Risk: "medium", DurationMS: 10}
	if err := s.RecordToolCall(tc); err != nil {
		t.Fatal(err)
	}
	ev := &Evaluation{SessionID: sessionID, TaskID: "t1", Evaluator: "tests", Outcome: "PASS", Detail: "3 tests"}
	if err := s.RecordEvaluation(ev); err != nil {
		t.Fatal(err)
	}
	if tcs, _ := s.ToolCalls(sessionID); len(tcs) != 1 {
		t.Fatalf("tool calls %d", len(tcs))
	}
	if evs, _ := s.EvaluationsForTask("t1"); len(evs) != 1 || evs[0].Outcome != "PASS" {
		t.Fatalf("evaluations %+v", evs)
	}
}

func TestAgentUpsertAndTranscript(t *testing.T) {
	s := openTest(t)
	a := &AgentRecord{ID: id.New(), SessionID: "s1", TaskID: "t1", Role: "implementer", Model: "m", Status: "running", Transcript: []byte(`[{"role":"user"}]`)}
	if err := s.UpsertAgent(a); err != nil {
		t.Fatal(err)
	}
	a.Status = "completed"
	a.Transcript = []byte(`[{"role":"user"},{"role":"assistant"}]`)
	if err := s.UpsertAgent(a); err != nil {
		t.Fatal(err)
	}
	got, err := s.Agent(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "completed" || string(got.Transcript) != string(a.Transcript) {
		t.Fatalf("got %+v", got)
	}
}

func TestCheckpoint(t *testing.T) {
	s := openTest(t)
	c := &Checkpoint{SessionID: "s1", TaskID: "t1", Reason: "pause", Payload: []byte(`{"x":1}`)}
	if err := s.SaveCheckpoint(c); err != nil {
		t.Fatal(err)
	}
	got, err := s.LatestCheckpoint("s1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Reason != "pause" || string(got.Payload) != `{"x":1}` {
		t.Fatalf("got %+v", got)
	}
}

func TestProjectMemoryUpsert(t *testing.T) {
	s := openTest(t)
	if err := s.PutMemory("proj-a", "instructions", "use go 1.27"); err != nil {
		t.Fatal(err)
	}
	if err := s.PutMemory("proj-a", "instructions", "use go 1.27 and bubbletea"); err != nil {
		t.Fatal(err)
	}
	m, err := s.GetMemory("proj-a", "instructions")
	if err != nil {
		t.Fatal(err)
	}
	if m.Content != "use go 1.27 and bubbletea" {
		t.Fatalf("content %q", m.Content)
	}
	all, err := s.ListMemory("proj-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(all))
	}
}

func TestConcurrentEventWrites(t *testing.T) {
	s := openTest(t)
	sessionID := id.New()
	done := make(chan struct{}, 8)
	for i := 0; i < 8; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			for j := 0; j < 25; j++ {
				e := event.New(&event.TaskCreatedData{Prompt: "x"})
				e.SessionID = sessionID
				if err := s.AppendEvent(sessionID, e); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	for i := 0; i < 8; i++ {
		<-done
	}
	evs, err := s.Events(sessionID, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 200 {
		t.Fatalf("expected 200 events, got %d", len(evs))
	}
}

func TestConcurrentMixedWritesDoNotError(t *testing.T) {
	s := openTest(t)
	// Hammer every write path from many goroutines; the write mutex must
	// serialize writers so SQLITE_BUSY never surfaces.
	var wg sync.WaitGroup
	errs := make(chan error, 400)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				id := fmt.Sprintf("g%d-%d", n, j)
				if err := s.CreateSession(&Session{ID: "s" + id, Title: id}); err != nil {
					errs <- err
				}
				if err := s.RecordModelCall(&ModelCall{ID: "m" + id, SessionID: "s" + id, Model: "x", Status: "ok"}); err != nil {
					errs <- err
				}
				if err := s.UpsertAgent(&AgentRecord{ID: "a" + id, SessionID: "s" + id, Role: "r", Status: "running"}); err != nil {
					errs <- err
				}
				if err := s.RecordToolCall(&ToolCall{ID: "t" + id, SessionID: "s" + id, Tool: "x", Status: "ok"}); err != nil {
					errs <- err
				}
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent write failed: %v", err)
	}
}
