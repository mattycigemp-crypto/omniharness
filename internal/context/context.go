// Package context builds model-specific context for agents. It never sends
// the whole repository or conversation to every model: it selects what is
// relevant, estimates token usage, and condenses when limits approach.
package context

import (
	"strings"

	"omniharness/internal/task"
)

// Message is a single conversational message (gateway-agnostic shape).
type Message struct {
	Role       string `json:"role"` // user | assistant | tool
	Content    string `json:"content"`
	ToolCallID string `json:"toolCallId,omitempty"`
	Name       string `json:"name,omitempty"` // tool name for tool results
}

// FileRef is a file selected for inclusion in context.
type FileRef struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// Input to the composer.
type Input struct {
	Spec       task.Spec
	Profile    task.Profile
	ProjectInstructions []string
	Files      []FileRef
	History    []Message // prior conversation (assistant messages + tool results)
	Summary    string    // running condensation summary
	// SystemPrompt is the base system prompt (role instructions etc.).
	SystemPrompt string
}

// Output of the composer.
type Output struct {
	Messages  []Message
	Tokens    int64
	Condensed bool
	Dropped   int
}

// Limits control composition behavior.
type Limits struct {
	// MaxTokens caps the total composed context. 0 = no cap.
	MaxTokens int64
	// CondenseAt is the token threshold at which old history is condensed.
	CondenseAt int64
}

// Composer assembles context for one model call.
type Composer struct {
	Limits Limits
}

// NewComposer returns a composer with the given limits.
func NewComposer(l Limits) *Composer { return &Composer{Limits: l} }

// Compose builds the message list for a model call.
func (c *Composer) Compose(in Input) (Output, error) {
	var out Output
	limit := c.Limits.MaxTokens
	if limit <= 0 {
		limit = 1 << 20 // practical safety cap
	}

	sys := in.SystemPrompt
	if sys == "" {
		sys = "You are a capable agent working on a task. Use the provided tools when they help."
	}
	if len(in.ProjectInstructions) > 0 {
		sys += "\n\nPROJECT INSTRUCTIONS:\n- " + strings.Join(in.ProjectInstructions, "\n- ")
	}
	if in.Summary != "" {
		sys += "\n\nSUMMARY OF PRIOR WORK:\n" + in.Summary
	}
	if p := in.Profile; p.Complexity != "" {
		sys += "\n\nTASK PROFILE: complexity=" + string(p.Complexity) +
			" domain=" + string(p.Domain) +
			" risk=" + string(p.Risk) +
			" verification=" + string(p.Verification)
		if len(p.Tools) > 0 {
			sys += " tools=" + strings.Join(p.Tools, ",")
		}
	}

	out.Messages = append(out.Messages, Message{Role: "system", Content: sys})
	out.Tokens += Estimate(sys)

	user := in.Spec.Prompt
	if len(in.Files) > 0 {
		var b strings.Builder
		b.WriteString(user)
		b.WriteString("\n\nRelevant files:\n")
		for _, f := range in.Files {
			// Cap per-file content to keep one file from blowing the budget.
			content := f.Content
			if len(content) > 30_000 {
				content = content[:30_000] + "\n...[truncated]"
			}
			b.WriteString("\n===== " + f.Path + " =====\n")
			b.WriteString(content)
			b.WriteString("\n===== end " + f.Path + " =====\n")
		}
		user = b.String()
	}
	out.Messages = append(out.Messages, Message{Role: "user", Content: user})
	out.Tokens += Estimate(user)

	// Append history until the cap is reached; condense the overflow.
	var kept []Message
	for _, m := range in.History {
		t := Estimate(m.Content)
		if out.Tokens+t > limit {
			out.Condensed = true
			out.Dropped += len(in.History) - len(kept)
			if c.Limits.CondenseAt > 0 && out.Tokens > c.Limits.CondenseAt {
				kept = append(kept, Message{Role: "system", Content: condensedMarker()})
			}
			break
		}
		kept = append(kept, m)
		out.Tokens += t
	}
	out.Messages = append(out.Messages, kept...)
	return out, nil
}

func condensedMarker() string {
	return "[Earlier conversation condensed. Rely on the SUMMARY OF PRIOR WORK and continue from the most recent state.]"
}

// Estimate approximates token count for a string (English-biased: ~4 chars
// per token, rune-based so it degrades gracefully for other scripts).
func Estimate(s string) int64 {
	if s == "" {
		return 0
	}
	n := int64(len([]rune(s)) / 4)
	if n == 0 {
		return 1
	}
	return n
}

// Summarize produces a compact summary of messages (used by the agent loop to
// maintain the running condensation summary).
func Summarize(messages []Message, maxChars int) string {
	if maxChars <= 0 {
		maxChars = 2000
	}
	var b strings.Builder
	for _, m := range messages {
		content := strings.TrimSpace(m.Content)
		if content == "" {
			continue
		}
		if len(content) > 300 {
			content = content[:300] + "…"
		}
		line := m.Role + ": " + content
		if b.Len()+len(line) > maxChars {
			break
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String())
}
