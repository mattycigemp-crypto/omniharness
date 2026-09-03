package task

import (
	"context"
	"testing"

	"omniharness/internal/model"
	"omniharness/internal/testutil"
)

func lowProfile() Profile {
	return Profile{Complexity: ComplexityLow, Ambiguity: LevelLow, Risk: LevelLow}
}

func TestWorthDeepeningSkipsLowEverything(t *testing.T) {
	if lowProfile().worthDeepening() {
		t.Fatal("a low-complexity, low-ambiguity, low-risk profile should not warrant deepening")
	}
}

func TestWorthDeepeningTriggersOnComplexity(t *testing.T) {
	p := lowProfile()
	p.Complexity = ComplexityHigh
	if !p.worthDeepening() {
		t.Fatal("high complexity should warrant deepening even with low ambiguity and risk")
	}
}

func TestWorthDeepeningTriggersOnAmbiguityEvenAtLowComplexity(t *testing.T) {
	p := lowProfile()
	p.Ambiguity = LevelHigh
	if !p.worthDeepening() {
		t.Fatal("high ambiguity should warrant deepening even at low complexity")
	}
}

func TestWorthDeepeningTriggersOnRisk(t *testing.T) {
	p := lowProfile()
	p.Risk = LevelHigh
	if !p.worthDeepening() {
		t.Fatal("high risk should warrant deepening")
	}
}

// A trivial profile must never spend a model call: this is the gate that
// keeps the pass from taxing the majority of requests that don't need it.
func TestDeepAnalyzeSkipsTheModelCallOnATrivialProfile(t *testing.T) {
	fake := testutil.NewFakeOmniRoute(t, testutil.FakeStep{StatusCode: 500}) // would fail loudly if ever called
	d := &DeepAnalyzer{Gateway: fake.Client(), ModelSel: model.NewSelector("fake/m1", nil)}

	result, err := d.Analyze(context.Background(), Spec{Prompt: "fix the typo"}, lowProfile())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Ran {
		t.Fatal("Ran = true, want the model never to have been called")
	}
	if fake.RequestCount() != 0 {
		t.Fatalf("gateway received %d requests, want 0", fake.RequestCount())
	}
	if result.Profile.AcceptanceCriteria != nil {
		t.Fatalf("AcceptanceCriteria = %v, want nil", result.Profile.AcceptanceCriteria)
	}
}

func TestDeepAnalyzeAddsAcceptanceCriteria(t *testing.T) {
	fake := testutil.NewFakeOmniRoute(t, testutil.FakeStep{
		Content: `["run go test ./... and see it pass", "the CLI still starts with no config file"]`,
	})
	d := &DeepAnalyzer{Gateway: fake.Client(), ModelSel: model.NewSelector("fake/m1", nil)}

	p := lowProfile()
	p.Complexity = ComplexityHigh
	result, err := d.Analyze(context.Background(), Spec{Prompt: "rewrite the config loader"}, p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Ran {
		t.Fatal("Ran = false, want true")
	}
	if len(result.Profile.AcceptanceCriteria) != 2 {
		t.Fatalf("AcceptanceCriteria = %v, want 2 entries", result.Profile.AcceptanceCriteria)
	}
	if result.TokensIn == 0 || result.TokensOut == 0 {
		t.Fatalf("usage not captured: in=%d out=%d", result.TokensIn, result.TokensOut)
	}
	// The rest of the profile must survive untouched.
	if result.Profile.Complexity != ComplexityHigh {
		t.Fatalf("Complexity = %s, want unchanged HIGH", result.Profile.Complexity)
	}
}

func TestDeepAnalyzeStripsAFencedCodeBlock(t *testing.T) {
	fake := testutil.NewFakeOmniRoute(t, testutil.FakeStep{
		Content: "```json\n[\"a real criterion\"]\n```",
	})
	d := &DeepAnalyzer{Gateway: fake.Client(), ModelSel: model.NewSelector("fake/m1", nil)}

	p := lowProfile()
	p.Risk = LevelHigh
	result, err := d.Analyze(context.Background(), Spec{Prompt: "delete the old migration table"}, p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Profile.AcceptanceCriteria) != 1 || result.Profile.AcceptanceCriteria[0] != "a real criterion" {
		t.Fatalf("AcceptanceCriteria = %v", result.Profile.AcceptanceCriteria)
	}
}

func TestDeepAnalyzeFallsBackOnUnparseableContent(t *testing.T) {
	fake := testutil.NewFakeOmniRoute(t, testutil.FakeStep{Content: "sure, here you go, no JSON at all"})
	d := &DeepAnalyzer{Gateway: fake.Client(), ModelSel: model.NewSelector("fake/m1", nil)}

	p := lowProfile()
	p.Risk = LevelHigh
	result, err := d.Analyze(context.Background(), Spec{Prompt: "delete the old migration table"}, p)
	if err != nil {
		t.Fatalf("unparseable content should not be reported as an error, got %v", err)
	}
	if !result.Ran {
		t.Fatal("Ran = false, want true — a request was made even though its content was unusable")
	}
	if result.Profile.AcceptanceCriteria != nil {
		t.Fatalf("AcceptanceCriteria = %v, want nil on unparseable content", result.Profile.AcceptanceCriteria)
	}
}

func TestDeepAnalyzeFallsBackOnAGatewayError(t *testing.T) {
	fake := testutil.NewFakeOmniRoute(t, testutil.FakeStep{Malformed: true})
	d := &DeepAnalyzer{Gateway: fake.Client(), ModelSel: model.NewSelector("fake/m1", nil)}

	p := lowProfile()
	p.Complexity = ComplexityHigh
	result, err := d.Analyze(context.Background(), Spec{Prompt: "rewrite the config loader"}, p)
	if err == nil {
		t.Fatal("a malformed gateway response should surface as an error")
	}
	if !result.Ran {
		t.Fatal("Ran = false, want true — a request was attempted")
	}
	if result.Profile.AcceptanceCriteria != nil {
		t.Fatalf("AcceptanceCriteria = %v, want nil on a failed call", result.Profile.AcceptanceCriteria)
	}
}

// A DeepAnalyzer with no gateway or selector configured must behave exactly
// like the gating skip: safe, unchanged, no panic. This is the state the
// zero value of the struct is in, and the state a config that never turns
// the feature on leaves it in.
func TestDeepAnalyzeIsSafeWithNoGatewayConfigured(t *testing.T) {
	d := &DeepAnalyzer{}
	p := lowProfile()
	p.Ambiguity = LevelHigh
	result, err := d.Analyze(context.Background(), Spec{Prompt: "clean up the code"}, p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Ran {
		t.Fatal("Ran = true, want false with no gateway configured")
	}
}
