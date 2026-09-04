package agent

import (
	"crypto/sha256"
	"encoding/hex"
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
	// staleSeenAt is how many times one call may return an identical result
	// before the model is told it already has that answer.
	staleSeenAt = 3
	// staleStreakStopAt is how many tool calls in a row may return nothing new
	// before the agent gives up. Deliberately generous: an agent re-reading
	// files it has already seen in order to summarise them is doing something
	// slightly wasteful, not stalling, and must not be cut off.
	staleStreakStopAt = 10
)

// repeatTracker remembers the current run of identical tool calls, and every
// call/result pair the run has already seen.
//
// The consecutive rule alone is not enough. A model that alternates two
// read-only calls never repeats itself consecutively and so never trips it,
// while learning exactly as little: watched live, a repair agent made 25 calls
// that between them returned three distinct results, byte for byte, until the
// budget ended the run. Keying on the result as well makes the "has anything
// changed?" question empirical rather than positional — and it is what keeps
// the guard safe, because a `go test` re-run after an edit produces different
// output and so is never a repeat.
type repeatTracker struct {
	last  string
	count int
	// seen counts call/result pairs; stale is the current run of calls that
	// returned something the run had already been told.
	seen  map[string]int
	stale int
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

// record files the result of a call that ran, and reports what to do about it.
// A nudge replaces the observation when the model has already been given this
// exact answer; stop means nothing new has come back for long enough that the
// run is going nowhere.
func (r *repeatTracker) record(tc gateway.ToolCall, obs string) (nudge string, stop bool) {
	if r.seen == nil {
		r.seen = map[string]int{}
	}
	sum := sha256.Sum256([]byte(obs))
	key := fingerprint(tc) + "\x00" + hex.EncodeToString(sum[:8])
	r.seen[key]++
	n := r.seen[key]
	if n == 1 {
		r.stale = 0
		return "", false
	}
	r.stale++
	if r.stale >= staleStreakStopAt {
		return "", true
	}
	if n >= staleSeenAt {
		return fmt.Sprintf(
			"same result as before: this %s call has now returned identical output %d times, "+
				"and that output is already above in this conversation. Nothing has changed. "+
				"Use what you already have — take a different action, or give your final answer.",
			tc.Function.Name, n), false
	}
	return "", false
}

// staleReason describes a run that stopped learning anything.
func (r *repeatTracker) staleReason() string {
	return fmt.Sprintf("stalled: %d tool calls in a row returned nothing that had not already been seen", r.stale)
}
