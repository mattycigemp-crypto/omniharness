// Package combo defines the model combos OmniHarness routes through. A
// "combo" is the user's chosen model strategy: either one of OmniRoute's
// auto/* routing combos (which resolve to whatever provider is actually
// provisioned) or a specific provider/model id. The list is derived from the
// live OmniRoute catalog when available, with a static fallback so the
// picker still works offline.
package combo

import (
	"fmt"
	"sort"
	"strings"
)

// Option is one selectable combo.
type Option struct {
	// ID is the value written to [models] default (e.g. auto/best-coding).
	ID string
	// Description is a one-line human explanation.
	Description string
	// Kind distinguishes routing combos from specific models.
	Kind string // "auto" | "model"
}

// descriptions for well-known auto/* combos. Unknown ids get a generic line.
var descriptions = map[string]string{
	"auto/best-coding":      "best provisioned model for coding work",
	"auto/best-reasoning":   "best provisioned model for deep reasoning",
	"auto/best-fast":        "fastest provisioned model",
	"auto/best-chat":        "best general chat model",
	"auto/best-coding-fast": "fast coding, still smart",
	"auto/best-vision":      "multimodal / vision",
	"auto/best-free":        "free tier",
	"auto/best-chaos":       "chaotic creativity",
	"auto/cheap":            "lowest cost",
	"auto/fast":             "low latency",
	"auto/smart":            "strong general intelligence",
	"auto/coding":           "coding work",
	"auto/reasoning":        "reasoning work",
	"auto/vision":           "vision / multimodal",
	"auto/chat":             "general chat",
	"auto/multimodal":       "multimodal",
	"auto/offline":          "local / offline models",
	"auto/claude-opus":      "Claude Opus class",
	"auto/claude-sonnet":    "Claude Sonnet class",
	"auto/gemini":           "Gemini class",
	"auto/llama":            "Llama class",
	"auto/gemma":            "Gemma class",
	"auto/glm":              "GLM class",
	"auto/mimo":             "MIMO class",
	"auto/minimax":          "MiniMax class",
	"auto/zai":              "ZAI class",
}

// fallback is the offline picker list (used when the catalog is unreachable).
var fallback = []string{
	"auto/best-coding", "auto/best-reasoning", "auto/best-fast",
	"auto/best-chat", "auto/best-coding-fast", "auto/best-vision",
	"auto/best-free", "auto/cheap", "auto/fast", "auto/smart",
	"auto/coding", "auto/reasoning", "auto/vision", "auto/chat",
	"auto/pro-coding", "auto/pro-reasoning", "auto/pro-fast", "auto/pro-chat",
}

// IsAuto reports whether id is an OmniRoute routing combo.
func IsAuto(id string) bool {
	return strings.HasPrefix(id, "auto/")
}

// Describe returns a best-effort description for a combo id.
func Describe(id string) string {
	if d, ok := descriptions[id]; ok {
		return d
	}
	if IsAuto(id) {
		if rest := strings.TrimPrefix(id, "auto/"); strings.HasPrefix(rest, "pro-") {
			return strings.TrimPrefix(rest, "pro-") + " — pro tier"
		}
		return "OmniRoute routing combo"
	}
	return "specific provider/model"
}

// List builds the ordered combo options for a catalog. auto/* combos come
// first (best-* family, then pro-*, then the rest alphabetically), followed
// by specific models alphabetically. An empty catalog (unreachable server)
// degrades to the static fallback so the picker always has choices.
func List(catalog []string) []Option {
	ids := catalog
	if len(ids) == 0 {
		ids = fallback
	}
	var autos, models []string
	for _, id := range ids {
		if IsAuto(id) {
			autos = append(autos, id)
		} else {
			models = append(models, id)
		}
	}
	sortAuto(autos)
	sort.Strings(models)

	out := make([]Option, 0, len(autos)+len(models))
	for _, id := range autos {
		out = append(out, Option{ID: id, Description: Describe(id), Kind: "auto"})
	}
	for _, id := range models {
		out = append(out, Option{ID: id, Description: Describe(id), Kind: "model"})
	}
	return out
}

// sortAuto orders auto/* combos: best-* first, then pro-*, then the rest.
func sortAuto(ids []string) {
	sort.Slice(ids, func(i, j int) bool {
		a, b := ids[i], ids[j]
		ra, rb := rank(a), rank(b)
		if ra != rb {
			return ra < rb
		}
		return a < b
	})
}

// rank groups auto ids: 0 = best-*, 1 = pro-*, 2 = everything else.
func rank(id string) int {
	switch {
	case strings.HasPrefix(id, "auto/best-"):
		return 0
	case strings.HasPrefix(id, "auto/pro-"):
		return 1
	default:
		return 2
	}
}

// FormatError explains why a combo id is invalid.
func FormatError(id string) string {
	return fmt.Sprintf("invalid model combo %q — expected provider/model (e.g. openai/gpt-5.4) or an auto/* combo (e.g. auto/best-coding)", id)
}
