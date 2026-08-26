package combo

import (
	"strings"
	"testing"
)

func TestListOrdersAutosFirstThenModels(t *testing.T) {
	opts := List([]string{"openai/gpt-5.4", "auto/chat", "auto/best-coding", "anthropic/sonnet"})
	if len(opts) != 4 {
		t.Fatalf("opts = %d", len(opts))
	}
	// auto/* must precede specific models.
	if !IsAuto(opts[0].ID) || !IsAuto(opts[1].ID) {
		t.Fatalf("autos not first: %+v", opts)
	}
	if opts[0].ID != "auto/best-coding" {
		t.Fatalf("best-* must rank first, got %q", opts[0].ID)
	}
	// Specific models sorted alphabetically.
	if opts[2].ID != "anthropic/sonnet" || opts[3].ID != "openai/gpt-5.4" {
		t.Fatalf("model order wrong: %+v", opts)
	}
}

func TestListFallbackWhenEmpty(t *testing.T) {
	opts := List(nil)
	if len(opts) == 0 {
		t.Fatal("empty catalog must fall back to built-in combos")
	}
	for _, o := range opts {
		if !IsAuto(o.ID) {
			t.Fatalf("fallback must contain only auto/* combos, got %q", o.ID)
		}
	}
	if !contains(opts, "auto/best-coding") {
		t.Fatal("fallback missing auto/best-coding")
	}
}

func TestDescribeKnownAndUnknown(t *testing.T) {
	if d := Describe("auto/best-coding"); !strings.Contains(d, "coding") {
		t.Errorf("Describe(auto/best-coding) = %q", d)
	}
	if d := Describe("auto/unknown-thing"); !strings.Contains(d, "routing combo") {
		t.Errorf("unknown auto id should get generic description: %q", d)
	}
	if d := Describe("auto/pro-vision"); !strings.Contains(d, "pro tier") {
		t.Errorf("pro combo should mention pro tier: %q", d)
	}
	if d := Describe("openai/gpt-5.4"); !strings.Contains(d, "provider/model") {
		t.Errorf("specific model description = %q", d)
	}
}

func TestFormatError(t *testing.T) {
	if !strings.Contains(FormatError("nope"), "provider/model") {
		t.Fatalf("FormatError = %q", FormatError("nope"))
	}
}

func contains(opts []Option, id string) bool {
	for _, o := range opts {
		if o.ID == id {
			return true
		}
	}
	return false
}
