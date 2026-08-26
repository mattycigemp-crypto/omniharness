package task

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Analyzer produces a TaskProfile for a Spec. It is deliberately cheap and
// deterministic: no model calls, pure text + filesystem heuristics.
type Analyzer struct {
	// RepoRoot, when set, enables repository-aware heuristics (git presence,
	// file counts) that inform the context estimate.
	RepoRoot string
}

var (
	reSentence = regexp.MustCompile(`[.!?]+\s+`)
	reNumbers  = regexp.MustCompile(`(?i)\b(\d+)\b`)
	reNewline  = regexp.MustCompile(`\n+`)
)

// Analyze produces the profile. Every judgment is recorded in
// Profile.Signals so decisions are auditable.
func (a *Analyzer) Analyze(s Spec) Profile {
	var sig []string
	add := func(f string, a ...any) { sig = append(sig, fmt.Sprintf(f, a...)) }

	text := strings.TrimSpace(s.Prompt)
	words := len(strings.Fields(text))
	sentences := len(reSentence.FindAllString(text, -1)) + 1
	clauses := len(reNewline.Split(text, -1))
	if clauses > 1 {
		sentences = clauses // bullet lists are more informative than prose
	}

	var p Profile
	p.Domain = detectDomain(text, add)
	p.Complexity = detectComplexity(text, words, sentences, add)
	p.Ambiguity = detectAmbiguity(text, add)
	p.Risk = detectRisk(text, add)
	p.Tools = detectTools(text, p.Domain, add)
	p.Parallelizable = detectParallelizable(text, sentences, add)
	p.Context = detectContext(text, words, a.RepoRoot, add)
	p.Verification = detectVerification(text, p.Domain, add)
	p.ApprovalRecommended = p.Risk == LevelHigh
	if p.ApprovalRecommended {
		add("approval recommended: risk is HIGH")
	}

	// Simple cost/latency model, calibrated roughly per token class. This is
	// an estimate for planning, not telemetry.
	weight := 1.0
	switch p.Complexity {
	case ComplexityMedium:
		weight = 3.0
	case ComplexityHigh:
		weight = 8.0
	}
	if p.Context == LevelLarge {
		weight *= 1.5
	}
	estTokens := float64(400+words*4) * weight
	p.EstimatedCostUSD = round4(estTokens * 3.0 / 1_000_000) // ~$3/M tokens blended
	latency := 5 + int64(estTokens/60)
	switch p.Complexity {
	case ComplexityMedium:
		latency *= 3
	case ComplexityHigh:
		latency *= 8
	}
	p.EstimatedLatencyMS = latency
	p.Confidence = confidence(p)
	add("estimated %d tokens, $%.4f, ~%ds", int(estTokens), p.EstimatedCostUSD, latency/1000)
	p.Signals = sig
	return p
}

func detectDomain(text string, add func(string, ...any)) Domain {
	lower := strings.ToLower(text)
	// Keywords carry weights: strong signals count double. Software terms are
	// weighted because they are highly specific; weak writing terms like
	// "readme" must not win over them.
	domains := []struct {
		d Domain
		w [][2]any
	}{
		{DomainSoftware, [][2]any{
			{"code", 2}, {"function", 2}, {"refactor", 2}, {"bug", 2}, {"fix", 1}, {"typo", 1}, {"compile", 2},
			{"test", 1}, {"api", 2}, {"class", 2}, {"package", 2}, {"repo", 2}, {"repository", 2}, {"git", 2},
			{"build", 2}, {"script", 1}, {"module", 2}, {"interface", 2}, {"implement", 1}, {"binary", 2},
			{"program", 1}, {"library", 2}, {"dependency", 2}, {"go ", 2}, {"rust", 2}, {"python", 2},
			{"typescript", 2}, {"javascript", 2}, {"crash", 2}, {"panic", 2}, {"exception", 2}, {"debug", 2},
			{"error", 1}, {"warning", 1},
		}},
		{DomainResearch, [][2]any{
			{"research", 2}, {"investigate", 2}, {"survey", 1}, {"literature", 2}, {"paper", 1}, {"compare", 1},
			{"findings", 2}, {"evidence", 1}, {"study", 1}, {"analysis of", 1}, {"analyze the", 1}, {"sources", 1},
		}},
		{DomainData, [][2]any{
			{"dataset", 2}, {"data", 1}, {"csv", 2}, {"sql", 2}, {"query", 1}, {"database", 1}, {"schema", 2},
			{"table", 1}, {"pipeline", 2}, {"etl", 2}, {"json file", 1}, {"export", 1},
		}},
		{DomainOps, [][2]any{
			{"deploy", 2}, {"server", 1}, {"infrastructure", 2}, {"docker", 2}, {"kubernetes", 2}, {"nginx", 2},
			{"migration", 2}, {"backup", 1}, {"monitor", 1}, {"incident", 2}, {"provision", 2}, {"terraform", 2},
			{"production", 1},
		}},
		{DomainWriting, [][2]any{
			{"write a", 2}, {"draft", 1}, {"documentation", 1}, {"docs", 1}, {"readme", 1}, {"blog", 2},
			{"article", 2}, {"report", 1}, {"proposal", 1}, {"email", 1},
		}},
	}
	best, bestScore := DomainGeneral, 0
	for _, dm := range domains {
		score := 0
		for _, kw := range dm.w {
			if strings.Contains(lower, kw[0].(string)) {
				score += kw[1].(int)
			}
		}
		if score > bestScore {
			best, bestScore = dm.d, score
		}
	}
	if bestScore > 0 {
		add("domain=%s (signal score %d)", best, bestScore)
	} else {
		add("domain=%s (no domain signals)", best)
	}
	return best
}

func detectComplexity(text string, words, sentences int, add func(string, ...any)) Complexity {
	lower := strings.ToLower(text)
	// Strong architectural signals count double per occurrence.
	heavy := []string{"architect", "design a", "multi-agent", "distributed", "concurrent", "entire system", "end-to-end", "comprehensive", "framework", "from scratch"}
	score := 0
	for _, w := range heavy {
		score += strings.Count(lower, w) * 2
	}
	// Feature-level signals count once per occurrence.
	mid := []string{"feature", "features", "sub-task", "subtasks", "module", "component"}
	for _, w := range mid {
		score += strings.Count(lower, w)
	}
	// Sequence markers ("first…then…finally…") indicate ordered multi-step work.
	score += strings.Count(lower, " then ")
	score += strings.Count(lower, " first ")
	score += strings.Count(lower, " finally")
	// Comma-separated requirement lists add weight.
	score += strings.Count(lower, ",")
	var c Complexity
	switch {
	case words < 25 && sentences <= 2 && score < 2:
		c = ComplexityLow
	case words < 160 && sentences <= 8 && score < 6:
		c = ComplexityMedium
	default:
		c = ComplexityHigh
	}
	add("complexity=%s (%d words, %d sentences, complexity score %d)", c, words, sentences, score)
	return c
}

func detectAmbiguity(text string, add func(string, ...any)) Level {
	lower := strings.ToLower(text)
	vague := []string{"something", "somehow", "maybe", "kind of", "improve", "clean up", "fix it", "make it better", "etc", "whatever", "as needed", "roughly"}
	score := 0
	for _, w := range vague {
		if strings.Contains(lower, w) {
			score++
		}
	}
	if strings.Contains(text, "?") {
		score++
	}
	// Precise signals reduce ambiguity.
	if strings.Contains(lower, "specifically") || strings.Contains(lower, "exactly") || reNumbers.MatchString(text) {
		score--
	}
	var l Level
	switch {
	case score >= 3:
		l = LevelHigh
	case score >= 1:
		l = LevelMedium
	default:
		l = LevelLow
	}
	add("ambiguity=%s (vague signals %d)", l, score)
	return l
}

func detectRisk(text string, add func(string, ...any)) Level {
	lower := strings.ToLower(text)
	destructive := []string{"delete", "drop ", "drop(", "remove all", "rm -rf", "overwrite", "destroy", "truncate", "wipe", "format ", "reset --hard", "force push", "push", "deploy", "publish", "release", "production", "prod", "migrate", "sudo", "chmod 777", "credentials", "secret", "token", "api key", "pay", "billing"}
	score := 0
	for _, w := range destructive {
		if strings.Contains(lower, w) {
			score++
		}
	}
	var l Level
	switch {
	case score >= 3:
		l = LevelHigh
	case score >= 1:
		l = LevelMedium
	default:
		l = LevelLow
	}
	if score > 0 {
		add("risk=%s (%d dangerous signals)", l, score)
	} else {
		add("risk=%s (no dangerous signals)", l)
	}
	return l
}

func detectTools(text string, d Domain, add func(string, ...any)) []string {
	lower := strings.ToLower(text)
	var tools []string
	needle := func(w string, t string) {
		if strings.Contains(lower, w) {
			tools = append(tools, t)
		}
	}
	needle("file", "filesystem")
	needle("directory", "filesystem")
	needle("folder", "filesystem")
	needle("write", "filesystem")
	needle("edit", "filesystem")
	needle("git", "git")
	needle("commit", "git")
	needle("run ", "shell")
	needle("command", "shell")
	needle("install", "shell")
	needle("build", "shell")
	needle("test", "shell")
	needle("search", "search")
	needle("find ", "search")
	needle("grep", "search")
	needle("kill", "process")
	needle("process", "process")
	if d == DomainSoftware && len(tools) == 0 {
		tools = append(tools, "filesystem", "search")
	}
	if len(tools) > 0 {
		add("tools=%v", tools)
	}
	return tools
}

func detectParallelizable(text string, sentences int, add func(string, ...any)) bool {
	lower := strings.ToLower(text)
	// Hard ordering words kill parallelism.
	ordered := []string{"then", "after that", "next,", "first ", "before", "depends on", "sequentially", "step by step", "in order", "finally"}
	for _, w := range ordered {
		if strings.Contains(lower, w) {
			add("parallelizable=false (ordering word %q)", w)
			return false
		}
	}
	// Multiple independent-sounding imperatives suggest parallelism.
	if sentences >= 3 && strings.Count(lower, " and ") >= 2 {
		add("parallelizable=true (%d clauses, multiple conjuncts)", sentences)
		return true
	}
	explicit := []string{"in parallel", "simultaneously", "concurrently", "both", "each of"}
	for _, w := range explicit {
		if strings.Contains(lower, w) {
			add("parallelizable=true (explicit %q)", w)
			return true
		}
	}
	add("parallelizable=false (no independent sub-tasks detected)")
	return false
}

func detectContext(text string, words int, repoRoot string, add func(string, ...any)) Level {
	score := 0
	if repoRoot != "" {
		entries, err := os.ReadDir(repoRoot)
		if err == nil {
			n := len(entries)
			if n > 500 {
				score += 3
			} else if n > 150 {
				score += 2
			} else if n > 30 {
				score += 1
			}
			if _, err := os.Stat(filepath.Join(repoRoot, ".git")); err == nil {
				score++
			}
			if score > 0 {
				add("context: repo has %d top-level entries", n)
			}
		}
	}
	var l Level
	switch {
	case words > 300 || score >= 2:
		l = LevelLarge
	case words > 100 || score >= 1:
		l = LevelMedium
	default:
		l = LevelSmall
	}
	add("context=%s (%d words, repo score %d)", l, words, score)
	return l
}

func detectVerification(text string, d Domain, add func(string, ...any)) Verification {
	lower := strings.ToLower(text)
	strict := []string{"must pass", "verify", "make sure it works", "ensure", "check", "validate", "run the tests", "ci", "prove"}
	for _, w := range strict {
		if strings.Contains(lower, w) {
			add("verification=REQUIRED (signal %q)", w)
			return VerificationRequired
		}
	}
	if d == DomainSoftware {
		add("verification=RECOMMENDED (software domain)")
		return VerificationRecommended
	}
	add("verification=NONE")
	return VerificationNone
}

func confidence(p Profile) float64 {
	c := 1.0
	if p.Ambiguity == LevelHigh {
		c -= 0.25
	}
	if p.Risk == LevelHigh {
		c -= 0.15
	}
	if p.Complexity == ComplexityHigh {
		c -= 0.1
	}
	if c < 0.4 {
		return 0.4
	}
	return c
}

func round4(f float64) float64 {
	return float64(int(f*10000+0.5)) / 10000
}
