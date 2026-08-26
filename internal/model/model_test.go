package model

import (
	"testing"
)

func TestResolvePreferred(t *testing.T) {
	s := NewSelector("cursor/claude-sonnet-5-max", map[string]string{"fast": "openai/gpt-5.4"})
	m, err := s.Resolve(Intent{Preferred: "gemini/gemini-2.5"})
	if err != nil {
		t.Fatal(err)
	}
	if m != "gemini/gemini-2.5" {
		t.Fatalf("model = %q", m)
	}
}

func TestResolveCapabilityOrder(t *testing.T) {
	s := NewSelector("cursor/claude-sonnet-5-max", map[string]string{
		"reasoning": "cursor/claude-opus-4-8-thinking-xhigh",
		"fast":      "openai/gpt-5.4",
		"cheap":     "",
	})
	m, err := s.Resolve(Intent{Capabilities: []string{CapCheap, CapFast}})
	if err != nil {
		t.Fatal(err)
	}
	if m != "openai/gpt-5.4" {
		t.Fatalf("model = %q", m)
	}
	// reasoning is not in the intent, so must not be used.
	m, _ = s.Resolve(Intent{Capabilities: []string{CapFast}})
	if m != "openai/gpt-5.4" {
		t.Fatalf("model = %q", m)
	}
}

func TestResolveFallsBackToDefault(t *testing.T) {
	s := NewSelector("cursor/claude-sonnet-5-max", map[string]string{})
	m, err := s.Resolve(Intent{Capabilities: []string{CapVision}})
	if err != nil {
		t.Fatal(err)
	}
	if m != "cursor/claude-sonnet-5-max" {
		t.Fatalf("model = %q", m)
	}
}

func TestResolveNoModelConfigured(t *testing.T) {
	s := NewSelector("", nil)
	if _, err := s.Resolve(Intent{}); err == nil {
		t.Fatal("expected error with no models configured")
	}
}

func TestValidateRef(t *testing.T) {
	if err := ValidateRef("provider/model"); err != nil {
		t.Fatal(err)
	}
	if err := ValidateRef("nope"); err == nil {
		t.Fatal("expected error")
	}
	if err := ValidateRef("/missing-provider"); err == nil {
		t.Fatal("expected error")
	}
	if err := ValidateRef("missing-model/"); err == nil {
		t.Fatal("expected error")
	}
}

func TestEstimateCost(t *testing.T) {
	// 1M in + 1M out on claude-opus ≈ $15 + $75 = $90.
	c := EstimateCost("cursor/claude-opus-4-8", 1_000_000, 1_000_000)
	if c < 89 || c > 91 {
		t.Fatalf("opus cost = %v", c)
	}
	// Fallback pricing for unknown models.
	c2 := EstimateCost("mystery/unknown", 1_000_000, 1_000_000)
	if c2 != 18 {
		t.Fatalf("fallback cost = %v", c2)
	}
	// Zero tokens = zero cost.
	if EstimateCost("x/y", 0, 0) != 0 {
		t.Fatal("zero tokens must be zero cost")
	}
}
