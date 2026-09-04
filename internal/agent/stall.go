package agent

import (
	"encoding/json"
	"fmt"

	"omniharness/internal/gateway"
	"omniharness/internal/tools"
)

// A model can get stuck re-issuing one tool call — writing the same file with
// the same contents, over and over — and the agent loop has no reason to stop
// it: every iteration returns tool calls, so the only exits are the budget and
// the iteration ceiling. The run then burns its whole duration allowance and
// reports a budget failure, hiding the fact that the work succeeded on the
// first call. This was watched happening against a live gateway.
//
// The guard counts *immediately consecutive* identical calls, and any different
// call resets it. That distinction matters: re-running `go test` after an edit
// is legitimate and must not be blocked, and it never is, because the edit sits
// between the two runs. Two identical calls back to back with nothing in
// between cannot have observed anything new.
const (
	// repeatNudgeAt is the run of identical calls at which the guard stops
	// executing them and tells the model what it is doing instead.
	repeatNudgeAt = 3
	// repeatStopAt is the run at which the agent gives up. Reached only when
	// the model ignores the nudge several times over.
	repeatStopAt = 6
)

// repeatTracker remembers the current run of identical tool calls.
type repeatTracker struct {
	last  string
	count int
}

// observe records a call and decides what to do with it. It returns a
// substitute observation when the call should not run, and stop when the agent
// should give up entirely.
func (r *repeatTracker) observe(tc gateway.ToolCall) (nudge string, stop bool) {
	fp := fingerprint(tc)
	if fp != r.last {
		r.last, r.count = fp, 1
		return "", false
	}
	r.count++
	switch {
	case r.count >= repeatStopAt:
		return "", true
	case r.count >= repeatNudgeAt:
		return fmt.Sprintf(
			"not executed: this is call %d of the identical %s call, with the same arguments, "+
				"and nothing has happened in between — the result cannot have changed. "+
				"Either take a different action or stop and give your final answer.",
			r.count, tc.Function.Name), false
	}
	return "", false
}

// reason describes the stall for the run's failure status.
func (r *repeatTracker) reason(name string) string {
	return fmt.Sprintf("stalled: repeated the same %s call %d times without making progress", name, r.count)
}

// fingerprint identifies a call by name and arguments. Arguments are decoded
// and re-encoded so that formatting differences between two otherwise identical
// calls do not read as progress; undecodable arguments fall back to their raw
// text.
func fingerprint(tc gateway.ToolCall) string {
	raw := tc.Function.Arguments
	if m, err := tools.DecodeArgs(raw); err == nil {
		if b, err := json.Marshal(m); err == nil {
			raw = string(b)
		}
	}
	return tc.Function.Name + "\x00" + raw
}
