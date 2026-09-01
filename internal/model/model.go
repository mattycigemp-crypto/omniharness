// Package model selects concrete provider/model references from capability
// requirements. OmniHarness reasons in terms of capabilities ("strongest
// reasoning", "fastest implementation", "lowest cost") and resolves them to
// OmniRoute's "provider/model" addressing at request time.
package model

import (
	"fmt"
	"strings"
)

// Capability names understood by the selection engine.
const (
	CapReasoning   = "reasoning"
	CapFast        = "fast"
	CapCheap       = "cheap"
	CapLongContext = "long-context"
	CapCoding      = "coding"
	CapVision      = "vision"
	CapResearch    = "research"
	CapReview      = "review"
)

// AllCapabilities lists every supported capability.
func AllCapabilities() []string {
	return []string{CapReasoning, CapFast, CapCheap, CapLongContext, CapCoding, CapVision, CapResearch, CapReview}
}

// Intent is the model-selection request. Preferred, when set, wins.
type Intent struct {
	// Preferred is an explicit "provider/model" reference.
	Preferred string
	// Capabilities in priority order; the first resolvable one wins.
	Capabilities []string
}

// Selector resolves capability intents against configuration.
type Selector struct {
	defaultModel string
	byCapability map[string]string
	// Empirical is an optional hook consulted after config resolution. When
	// it returns ok, its alternative model and reason replace the resolved
	// choice. It lets performance memory influence selection without coupling
	// this package to the store.
	Empirical func(resolved string, candidates []string) (alt, reason string, ok bool)
}

// NewSelector builds a selector from config values.
func NewSelector(defaultModel string, byCapability map[string]string) *Selector {
	cap := make(map[string]string, len(byCapability))
	for k, v := range byCapability {
		cap[k] = v
	}
	return &Selector{defaultModel: defaultModel, byCapability: cap}
}

// AllConfigured returns the deduplicated set of configured model references
// (default + every capability), in a stable order. These are the candidates
// an empirical hook may choose among.
func (s *Selector) AllConfigured() []string {
	seen := map[string]bool{}
	var out []string
	add := func(m string) {
		if m != "" && !seen[m] {
			seen[m] = true
			out = append(out, m)
		}
	}
	add(s.defaultModel)
	for _, c := range AllCapabilities() {
		add(s.byCapability[c])
	}
	return out
}

// Resolve turns an intent into a "provider/model" reference.
func (s *Selector) Resolve(in Intent) (string, error) {
	m, _, err := s.ResolveExplain(in)
	return m, err
}

// ResolveExplain resolves like Resolve but also returns a human-readable
// reason for the choice — the config basis, or the empirical substitute.
func (s *Selector) ResolveExplain(in Intent) (string, string, error) {
	var base string
	var basis string
	if in.Preferred != "" {
		if err := ValidateRef(in.Preferred); err != nil {
			return "", "", err
		}
		base, basis = in.Preferred, "explicit model reference"
	} else {
		for _, c := range in.Capabilities {
			if m := s.byCapability[c]; m != "" {
				base, basis = m, "config capability \""+c+"\""
				break
			}
		}
		if base == "" && s.defaultModel != "" {
			base, basis = s.defaultModel, "config default"
		}
	}
	if base == "" {
		return "", "", fmt.Errorf("no model configured and no default set")
	}
	if s.Empirical != nil {
		if alt, reason, ok := s.Empirical(base, s.AllConfigured()); ok && alt != "" {
			return alt, reason, nil
		}
	}
	return base, "selected by " + basis, nil
}

// ValidateRef checks that a reference has the form provider/model.
func ValidateRef(ref string) error {
	if !strings.Contains(ref, "/") {
		return fmt.Errorf("model reference %q must be provider/model", ref)
	}
	p, m := SplitRef(ref)
	if p == "" || m == "" {
		return fmt.Errorf("model reference %q must be provider/model", ref)
	}
	return nil
}

// SplitRef splits a provider/model reference.
func SplitRef(ref string) (provider, model string) {
	i := strings.Index(ref, "/")
	if i < 0 {
		return "", ref
	}
	return ref[:i], ref[i+1:]
}

// Pricing is the per-1M-token cost for a model family (USD). Used only for
// estimates; actual billing is OmniRoute's business.
var pricing = []struct {
	prefix string
	in     float64
	out    float64
}{
	{"claude-opus", 15.0, 75.0},
	{"claude-sonnet", 3.0, 15.0},
	{"claude-haiku", 0.8, 4.0},
	{"gpt-5.5", 1.25, 10.0},
	{"gpt-5.4", 1.25, 10.0},
	{"gpt-5", 1.25, 10.0},
	{"gemini", 1.25, 5.0},
	{"deepseek", 0.27, 1.10},
	{"agnes", 0.15, 0.60},
	{"llama", 0.20, 0.60},
	{"qwen", 0.15, 0.60},
	{"grok", 3.0, 15.0},
}

// EstimateCost estimates the USD cost of a model call from the model name
// (which includes the provider prefix) and token counts.
func EstimateCost(modelRef string, tokensIn, tokensOut int64) float64 {
	_, modelName := SplitRef(modelRef)
	lower := strings.ToLower(modelName)
	in, out := 3.0, 15.0 // fallback: blended mid-tier pricing
	for _, p := range pricing {
		if strings.Contains(lower, p.prefix) {
			in, out = p.in, p.out
			break
		}
	}
	return round4(float64(tokensIn)/1e6*in + float64(tokensOut)/1e6*out)
}

func round4(f float64) float64 {
	return float64(int(f*10000+0.5)) / 10000
}
