package memory

import (
	"sort"
	"strings"
	"unicode"

	"omniharness/internal/session"
)

// DefaultRecallLimit caps how many remembered notes are carried into an
// agent's prompt. Recall used to be unbounded: every note ever remembered
// about a workspace went into every agent's system prompt on every task.
// That is fine for five notes and actively harmful at fifty — the genuinely
// relevant one is buried in noise it has to be read past, and the whole set
// is paid for in tokens on every single model call of every step.
const DefaultRecallLimit = 8

// minTermLen ignores very short tokens ("a", "is", "to"), which match
// everything and therefore rank nothing.
const minTermLen = 3

// stopWords are function words that clear minTermLen but carry no topical
// signal. Without this list a note that merely contains "the" scores as if
// it shared subject matter with the task — which is how a note whose own
// kind named the task's subject lost to one that just happened to use an
// article. Deliberately short: it holds function words only, so domain
// terms that happen to be brief ("api", "css", "git", "log", "npm") keep
// their full weight.
var stopWords = map[string]bool{
	"the": true, "and": true, "for": true, "that": true, "this": true,
	"with": true, "from": true, "into": true, "are": true, "was": true,
	"were": true, "but": true, "not": true, "all": true, "any": true,
	"has": true, "have": true, "had": true, "will": true, "would": true,
	"should": true, "when": true, "then": true, "than": true, "they": true,
	"them": true, "their": true, "there": true, "here": true, "what": true,
	"which": true, "who": true, "how": true, "why": true, "its": true,
	"our": true, "your": true, "you": true, "about": true, "been": true,
	"also": true, "just": true, "only": true, "some": true, "such": true,
	"each": true, "every": true, "more": true, "most": true, "very": true,
}

// Relevant ranks remembered notes against a task's prompt and returns at
// most limit of them, plus whether anything was left out.
//
// The ranking is lexical — shared terms between the task and the note, with
// a note's own kind weighted higher because a kind that names something in
// the task ("test-setup" for a task about tests) is a strong signal. It is
// deliberately not embedding-based: vector databases are an explicit
// anti-goal of this project, and a local, dependency-free ranking that is
// obviously right most of the time beats a semantic one that needs an
// embedding endpoint, a store, and a migration to be right slightly more
// often.
//
// Because the ranking is crude, it never lets a low score exclude a note
// while capacity remains: once the notes that share terms with the task are
// taken, the rest fill the remaining slots in their existing order. A note
// can be relevant while sharing no vocabulary with the request, and the cap
// exists to bound noise, not to enforce a judgment this scoring cannot make.
func Relevant(rows []session.ProjectMemory, query string, limit int) ([]session.ProjectMemory, bool) {
	if limit <= 0 {
		limit = DefaultRecallLimit
	}
	if len(rows) <= limit {
		// Nothing to choose between: return them untouched rather than
		// imposing a ranking nobody asked for.
		return rows, false
	}

	terms := termSet(query)
	if len(terms) == 0 {
		// No usable query terms: take the first `limit` in their existing
		// order rather than pretending to have ranked them.
		return rows[:limit], true
	}

	type scored struct {
		row   session.ProjectMemory
		score int
		order int
	}
	all := make([]scored, 0, len(rows))
	for i, r := range rows {
		all = append(all, scored{row: r, score: score(r, terms), order: i})
	}
	// Highest score first; ties keep their original order, so the same
	// memories and the same task always produce the same prompt.
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].score != all[j].score {
			return all[i].score > all[j].score
		}
		return all[i].order < all[j].order
	})

	out := make([]session.ProjectMemory, 0, limit)
	for _, s := range all[:limit] {
		out = append(out, s.row)
	}
	return out, true
}

// score counts how many distinct query terms a note mentions, counting a
// match in its kind twice.
func score(m session.ProjectMemory, terms map[string]bool) int {
	content := termSet(m.Content)
	kind := termSet(m.Kind)
	n := 0
	for t := range terms {
		if kind[t] {
			n += 2
		} else if content[t] {
			n++
		}
	}
	return n
}

// termSet splits text into a set of lowercase terms, treating anything that
// is not a letter or digit as a separator so "test-setup" and "test setup"
// produce the same terms.
func termSet(s string) map[string]bool {
	fields := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	out := make(map[string]bool, len(fields))
	for _, f := range fields {
		if len(f) >= minTermLen && !stopWords[f] {
			out[f] = true
		}
	}
	return out
}
