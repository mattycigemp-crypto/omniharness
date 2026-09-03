package task

import (
	"context"
	"encoding/json"
	"strings"

	"omniharness/internal/gateway"
	"omniharness/internal/model"
)

// DeepAnalyzer optionally deepens a heuristic Profile with a single model
// call. Analyzer.Analyze is pure and free; this is neither — it spends
// tokens and can fail over the network — so every call is gated by
// worthDeepening, bounded to one attempt, and falls back to the heuristic
// Profile unchanged on any error or unusable response. Nothing downstream
// may assume AcceptanceCriteria is present.
type DeepAnalyzer struct {
	Gateway  *gateway.Client
	ModelSel *model.Selector
}

// worthDeepening reports whether p is a strong enough candidate for a
// deepening call to be worth its cost. Low-complexity, low-risk,
// unambiguous tasks — the majority of requests — are skipped entirely: the
// pure analyzer's own signals already say there is nothing here for a model
// to usefully add, and calling one anyway would tax every trivial task to
// benefit none of them.
func (p Profile) worthDeepening() bool {
	return p.Complexity != ComplexityLow || p.Ambiguity == LevelHigh || p.Risk == LevelHigh
}

// DeepResult is the outcome of one DeepAnalyzer.Analyze call. It carries
// enough usage detail for a caller to account the call's cost against a
// task budget and record it in the store — the same bookkeeping every other
// model call gets — so a deepening pass is never a hidden, unaccounted
// cost. Ran is false whenever no request was actually sent (gated skip,
// unresolved model); a caller should skip accounting entirely in that case.
type DeepResult struct {
	Profile   Profile
	Ran       bool
	Model     string
	TokensIn  int64
	TokensOut int64
}

// Analyze calls the model once to produce concrete acceptance criteria for
// spec, grounded in the actual request rather than a generic checklist, and
// returns a DeepResult carrying profile with AcceptanceCriteria filled in.
// profile's other fields are never touched. The returned error is
// informational only — every failure path (gating skip, unconfigured
// dependencies, a model error, unparseable output) returns a Profile
// identical to the input, so a caller that ignores the error still gets a
// safe, usable Profile; the error exists so a caller that wants to log or
// count deepening failures can.
func (d *DeepAnalyzer) Analyze(ctx context.Context, spec Spec, profile Profile) (DeepResult, error) {
	if !profile.worthDeepening() {
		return DeepResult{Profile: profile}, nil
	}
	if d == nil || d.Gateway == nil || d.ModelSel == nil {
		return DeepResult{Profile: profile}, nil
	}
	modelRef, err := d.ModelSel.Resolve(model.Intent{Capabilities: []string{model.CapFast, model.CapReasoning}})
	if err != nil {
		return DeepResult{Profile: profile}, err
	}
	prompt := "Task request:\n" + spec.Prompt + "\n\n" +
		"List 3 to 6 concrete, checkable acceptance criteria for this task — " +
		"specific statements that would let someone verify the work was done " +
		"correctly, grounded in what this request actually asks for. Do not " +
		"restate the request itself as a criterion. Respond with a JSON array " +
		"of strings and nothing else."
	resp, err := d.Gateway.Chat(ctx, gateway.ChatRequest{
		Model:    modelRef,
		Messages: []gateway.Message{{Role: "user", Content: prompt}},
	})
	if err != nil {
		return DeepResult{Profile: profile, Ran: true, Model: modelRef}, err
	}
	result := DeepResult{
		Profile: profile, Ran: true, Model: modelRef,
		TokensIn: resp.Usage.PromptTokens, TokensOut: resp.Usage.CompletionTokens,
	}
	if len(resp.Choices) == 0 {
		return result, nil
	}
	criteria, ok := parseAcceptanceCriteria(resp.Choices[0].Message.Content)
	if !ok {
		return result, nil
	}
	result.Profile.AcceptanceCriteria = criteria
	return result, nil
}

// parseAcceptanceCriteria extracts a JSON string array from a model
// response, tolerating a fenced code block around it — the common way a
// model wraps "respond with JSON and nothing else" despite the instruction.
// Blank entries are dropped; an empty or unparseable result reports false so
// the caller leaves the Profile's existing AcceptanceCriteria (nil) alone.
func parseAcceptanceCriteria(content string) ([]string, bool) {
	s := strings.TrimSpace(content)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	s = strings.TrimSpace(s)
	var raw []string
	if err := json.Unmarshal([]byte(s), &raw); err != nil {
		return nil, false
	}
	cleaned := make([]string, 0, len(raw))
	for _, c := range raw {
		c = strings.TrimSpace(c)
		if c != "" {
			cleaned = append(cleaned, c)
		}
	}
	if len(cleaned) == 0 {
		return nil, false
	}
	return cleaned, true
}
