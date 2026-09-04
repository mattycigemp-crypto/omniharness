package memory

import (
	"testing"

	"omniharness/internal/session"
)

func notes(pairs ...string) []session.ProjectMemory {
	var out []session.ProjectMemory
	for i := 0; i < len(pairs); i += 2 {
		out = append(out, session.ProjectMemory{Kind: pairs[i], Content: pairs[i+1]})
	}
	return out
}

// Under the limit there is nothing to choose between, so the notes come back
// untouched — same set, same order. Recall behaves exactly as it did before
// ranking existed for every project small enough not to need it.
func TestRelevantLeavesSmallSetsAlone(t *testing.T) {
	in := notes("a", "one", "b", "two", "c", "three")
	got, truncated := Relevant(in, "something unrelated entirely", 8)
	if truncated {
		t.Fatal("a set under the limit must not report truncation")
	}
	if len(got) != len(in) {
		t.Fatalf("got %d notes, want all %d", len(got), len(in))
	}
	for i := range got {
		if got[i].Kind != in[i].Kind {
			t.Fatalf("order changed at %d: %q vs %q", i, got[i].Kind, in[i].Kind)
		}
	}
}

func TestRelevantRanksSharedTermsFirst(t *testing.T) {
	in := notes(
		"deploy", "run the deploy script from the release branch",
		"styling", "two-space indentation in the stylesheets",
		"test-setup", "the integration tests need a running postgres",
		"license", "every file carries the MIT header",
	)
	got, truncated := Relevant(in, "the integration tests are failing, fix postgres setup", 2)
	if !truncated {
		t.Fatal("cutting 4 notes to 2 must report truncation")
	}
	if len(got) != 2 {
		t.Fatalf("got %d notes, want 2", len(got))
	}
	if got[0].Kind != "test-setup" {
		t.Fatalf("top note = %q, want the one sharing terms with the task", got[0].Kind)
	}
}

// A note whose kind names something in the task outranks one that merely
// mentions it in passing.
func TestRelevantWeightsTheKind(t *testing.T) {
	in := notes(
		"general", "the word deploy appears here but this note is about nothing",
		"deploy", "unrelated wording entirely",
		"filler1", "x", "filler2", "x", "filler3", "x",
	)
	got, _ := Relevant(in, "deploy the service", 1)
	if got[0].Kind != "deploy" {
		t.Fatalf("top note = %q, want the kind match to win", got[0].Kind)
	}
}

// The scoring is crude, so it must never let a zero score exclude a note
// while there is still room: relevance can exist without shared vocabulary.
func TestRelevantFillsRemainingSlotsWithUnmatchedNotes(t *testing.T) {
	in := notes(
		"unrelated1", "aaa", "unrelated2", "bbb",
		"match", "postgres", "unrelated3", "ccc",
	)
	got, _ := Relevant(in, "postgres", 3)
	if len(got) != 3 {
		t.Fatalf("got %d notes, want the limit filled", len(got))
	}
	if got[0].Kind != "match" {
		t.Fatalf("top note = %q, want the match first", got[0].Kind)
	}
}

// Same notes plus same task must always produce the same prompt.
func TestRelevantIsDeterministic(t *testing.T) {
	in := notes("a", "x", "b", "x", "c", "x", "d", "x", "e", "x")
	first, _ := Relevant(in, "nothing matches this", 3)
	for i := 0; i < 20; i++ {
		again, _ := Relevant(in, "nothing matches this", 3)
		for j := range first {
			if first[j].Kind != again[j].Kind {
				t.Fatalf("run %d differed at %d: %q vs %q", i, j, first[j].Kind, again[j].Kind)
			}
		}
	}
}

// An empty prompt cannot rank anything; take the first notes rather than
// inventing an order.
func TestRelevantWithNoUsableQueryTerms(t *testing.T) {
	in := notes("a", "x", "b", "x", "c", "x")
	got, truncated := Relevant(in, "a to is", 2)
	if !truncated || len(got) != 2 {
		t.Fatalf("got %d notes (truncated=%v), want 2", len(got), truncated)
	}
	if got[0].Kind != "a" || got[1].Kind != "b" {
		t.Fatalf("got %q,%q — want the original order preserved", got[0].Kind, got[1].Kind)
	}
}

func TestRelevantDefaultsTheLimit(t *testing.T) {
	var in []session.ProjectMemory
	for i := 0; i < DefaultRecallLimit+5; i++ {
		in = append(in, session.ProjectMemory{Kind: "k", Content: "c"})
	}
	got, truncated := Relevant(in, "q", 0)
	if !truncated || len(got) != DefaultRecallLimit {
		t.Fatalf("got %d notes (truncated=%v), want %d", len(got), truncated, DefaultRecallLimit)
	}
}
