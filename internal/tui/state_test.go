package tui

import (
	"context"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"omniharness/internal/event"
	"omniharness/internal/policy"
	"omniharness/internal/tools"
)

func TestTruncateKeepsValidUTF8(t *testing.T) {
	// truncate is applied to user prompts, model errors and the session
	// title, which is persisted. Cutting on a byte boundary produces a
	// mangled rune in the TUI and a corrupt title in the store.
	for _, tc := range []struct {
		name string
		in   string
		n    int
	}{
		{"accents", strings.Repeat("é", 40), 20},
		{"emoji", strings.Repeat("🙂", 20), 15},
		{"cjk", strings.Repeat("日本語", 20), 10},
		{"mixed", "résumé " + strings.Repeat("🙂", 10) + " done", 12},
	} {
		got := truncate(tc.in, tc.n)
		if !utf8.ValidString(got) {
			t.Errorf("%s: truncate(%q, %d) = %q, which is not valid UTF-8", tc.name, tc.in, tc.n, got)
		}
	}
}

func TestTruncateCountsCharactersNotBytes(t *testing.T) {
	// A limit of 10 should mean ten visible characters whether the text is
	// ASCII or not, otherwise a Japanese title is cut to a third the length
	// of an English one.
	got := truncate("日本語日本語日本語日本語", 10)
	if runes := utf8.RuneCountInString(strings.TrimSuffix(got, "…")); runes != 10 {
		t.Errorf("truncate(...) kept %d runes, want 10: %q", runes, got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("a truncated string must be marked: %q", got)
	}
}

func TestTruncateLeavesShortStringsAlone(t *testing.T) {
	for _, s := range []string{"", "short", "日本語"} {
		if got := truncate(s, 10); got != s {
			t.Errorf("truncate(%q, 10) = %q, want it unchanged", s, got)
		}
	}
	// Newlines would break the single-line rows this feeds.
	if got := truncate("two\nlines", 20); got != "two lines" {
		t.Errorf("truncate collapsed newlines to %q", got)
	}
}

func TestShortID(t *testing.T) {
	if got := shortID("0123456789abcdef"); got != "01234567" {
		t.Errorf("shortID = %q, want 8 characters", got)
	}
	for _, s := range []string{"", "abc", "01234567"} {
		if got := shortID(s); got != s {
			t.Errorf("shortID(%q) = %q, want it unchanged", s, got)
		}
	}
}

func TestRecordModelStatCreatesThenUpdates(t *testing.T) {
	m := &Model{modelStatID: map[string]int{}}

	m.recordModelStat("openai/gpt-5", func(s *modelStat) { s.Calls++ })
	m.recordModelStat("openai/gpt-5", func(s *modelStat) { s.Calls++ })
	m.recordModelStat("anthropic/claude", func(s *modelStat) { s.Calls++ })

	if len(m.modelStats) != 2 {
		t.Fatalf("got %d rows, want one per model", len(m.modelStats))
	}
	if m.modelStats[0].ID != "openai/gpt-5" || m.modelStats[0].Calls != 2 {
		t.Errorf("first row = %+v, want gpt-5 with 2 calls", m.modelStats[0])
	}
	// First seen, first shown: the rail must not reshuffle as stats arrive.
	if m.modelStats[1].ID != "anthropic/claude" {
		t.Errorf("second row = %q, want the order models first appeared", m.modelStats[1].ID)
	}

	// An empty id would create a blank lane that never updates again.
	m.recordModelStat("", func(s *modelStat) { s.Calls = 99 })
	if len(m.modelStats) != 2 {
		t.Errorf("an empty model id added a row: %+v", m.modelStats)
	}
}

func TestUpsertAgentOnlyTouchesTheMatchingLane(t *testing.T) {
	m := &Model{agents: []agentRow{
		{ID: shortID("aaaaaaaaaaaa"), Role: "implementer", Model: "m1", State: "created"},
		{ID: shortID("bbbbbbbbbbbb"), Role: "reviewer", Model: "m2", State: "created"},
	}}

	m.upsertAgent("aaaaaaaaaaaa", event.AgentStateData{
		Status: "running", Action: "editing", Model: "m9", Tokens: 100, CostUSD: 0.5,
	})
	if got := m.agents[0]; got.State != "running" || got.Action != "editing" || got.Model != "m9" || got.Tokens != 100 {
		t.Errorf("target lane = %+v", got)
	}
	if m.agents[1].State != "created" {
		t.Errorf("the other lane changed: %+v", m.agents[1])
	}

	// Zero-valued fields mean "no news", not "reset to zero" — a status-only
	// update must not wipe the totals already shown.
	m.upsertAgent("aaaaaaaaaaaa", event.AgentStateData{Status: "completed"})
	if got := m.agents[0]; got.Tokens != 100 || got.Cost != 0.5 || got.Action != "editing" || got.Model != "m9" {
		t.Errorf("a status-only update cleared earlier values: %+v", got)
	}
	if m.agents[0].State != "completed" {
		t.Errorf("state = %q, want completed", m.agents[0].State)
	}
}

func TestPushEventIsBounded(t *testing.T) {
	m := &Model{}
	for i := 0; i < 700; i++ {
		m.pushEvent(event.Event{Type: event.TaskStarted, Time: time.Now()})
	}
	if len(m.events) != 600 {
		t.Errorf("event log grew to %d, want it capped at 600", len(m.events))
	}
}

func TestChatIsBoundedAndSkipsEmpty(t *testing.T) {
	m := &Model{}
	m.chat(chatUser, "")
	if len(m.conversation) != 0 {
		t.Error("an empty line must not be added to the transcript")
	}
	for i := 0; i < 500; i++ {
		m.chat(chatUser, "line")
	}
	if len(m.conversation) != 400 {
		t.Errorf("transcript grew to %d, want it capped at 400", len(m.conversation))
	}
}

func TestAwaitApprovalDeniesUnlessExplicitlyGranted(t *testing.T) {
	// Anything other than a human saying yes is a denial. The approval gate
	// is the last thing standing between a risky tool and the workspace.
	t.Run("granted", func(t *testing.T) {
		reply := make(chan bool, 1)
		reply <- true
		got, err := awaitApproval(context.Background(), reply, time.Minute)
		if err != nil || !got {
			t.Fatalf("= %v, %v; want true, nil", got, err)
		}
	})

	t.Run("denied", func(t *testing.T) {
		reply := make(chan bool, 1)
		reply <- false
		got, err := awaitApproval(context.Background(), reply, time.Minute)
		if err != nil || got {
			t.Fatalf("= %v, %v; want false, nil", got, err)
		}
	})

	t.Run("timed out", func(t *testing.T) {
		got, err := awaitApproval(context.Background(), make(chan bool), 10*time.Millisecond)
		if err != nil || got {
			t.Fatalf("= %v, %v; want false, nil — an unanswered prompt is not a grant", got, err)
		}
	})

	t.Run("cancelled", func(t *testing.T) {
		// Cancelling a task while a prompt is open must return at once. This
		// used to ignore the context and sit on the five-minute timeout, so
		// ctrl-c appeared to hang.
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		done := make(chan struct{})
		var got bool
		var err error
		go func() {
			got, err = awaitApproval(ctx, make(chan bool), 5*time.Minute)
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("a cancelled context must not wait for the approval timeout")
		}
		if got {
			t.Error("a cancelled approval must not be a grant")
		}
		if err == nil {
			t.Error("a cancelled approval must report why it stopped")
		}
	})
}

func TestRequestApprovalDeniesWithNoUIAttached(t *testing.T) {
	// Headless, or before the program starts: there is no one to ask, so the
	// answer is no rather than a silent yes.
	a := &approvalApprover{model: &Model{}}
	got, err := a.RequestApproval(context.Background(), policyRequestForTest(), "high risk")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got {
		t.Error("an approval with no UI attached must be denied")
	}
}

func policyRequestForTest() policy.Request {
	return policy.Request{Tool: "write_file", Risk: tools.RiskHigh}
}
