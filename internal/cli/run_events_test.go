package cli

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"omniharness/internal/event"
)

func toolFinished(t *testing.T, d event.ToolFinishedData) event.Event {
	t.Helper()
	raw, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	return event.Event{Type: event.ToolCompleted, Data: raw}
}

// Every tool outcome arrives as ToolCompleted, carrying the real result in
// Status. The printer ignored Status and said "ok" for all of them, so a
// headless run reported policy denials as successful work — which is exactly
// what a live run against a real gateway showed: eight denied write_file
// calls, every one of them printed as "tool ok", and no file on disk.
func TestHeadlessOutputDoesNotCallADeniedToolOK(t *testing.T) {
	got := formatEvent(toolFinished(t, event.ToolFinishedData{
		Tool: "write_file", Status: "denied", Duration: 0,
	}))
	if strings.Contains(got, "ok") {
		t.Fatalf("denied tool rendered as %q", got)
	}
	if !strings.Contains(got, "denied") || !strings.Contains(got, "write_file") {
		t.Fatalf("rendered %q, want it to name the tool and say it was denied", got)
	}
}

func TestHeadlessOutputReportsFailedAndCancelledTools(t *testing.T) {
	failed := formatEvent(toolFinished(t, event.ToolFinishedData{
		Tool: "shell", Status: "failed", Error: "exit status 1",
	}))
	if !strings.Contains(failed, "failed") || !strings.Contains(failed, "exit status 1") {
		t.Errorf("failed tool rendered as %q", failed)
	}

	cancelled := formatEvent(toolFinished(t, event.ToolFinishedData{Tool: "git", Status: "cancelled"}))
	if strings.Contains(cancelled, "ok") {
		t.Errorf("cancelled tool rendered as %q", cancelled)
	}
}

// A genuinely completed tool must still read as ok, with its duration.
func TestHeadlessOutputStillReportsCompletedTools(t *testing.T) {
	got := formatEvent(toolFinished(t, event.ToolFinishedData{
		Tool: "read_file", Status: "completed", Duration: 1500 * time.Millisecond,
	}))
	if !strings.Contains(got, "ok") || !strings.Contains(got, "read_file") {
		t.Fatalf("completed tool rendered as %q", got)
	}
}
