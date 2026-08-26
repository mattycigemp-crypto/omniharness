package context

import (
	"strings"
	"testing"

	"omniharness/internal/task"
)

func TestComposeBasic(t *testing.T) {
	c := NewComposer(Limits{})
	out, err := c.Compose(Input{
		Spec:    task.Spec{Prompt: "Implement a parser."},
		Profile: task.Profile{Complexity: task.ComplexityMedium, Domain: task.DomainSoftware},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Messages) < 2 {
		t.Fatalf("expected system+user, got %d", len(out.Messages))
	}
	if out.Messages[0].Role != "system" {
		t.Fatalf("first message %q", out.Messages[0].Role)
	}
	if !strings.Contains(out.Messages[0].Content, "TASK PROFILE") {
		t.Fatalf("system prompt missing profile: %q", out.Messages[0].Content)
	}
	if out.Messages[1].Role != "user" || !strings.Contains(out.Messages[1].Content, "parser") {
		t.Fatalf("user message: %+v", out.Messages[1])
	}
}

func TestProjectInstructionsIncluded(t *testing.T) {
	c := NewComposer(Limits{})
	out, _ := c.Compose(Input{
		Spec:                task.Spec{Prompt: "x"},
		ProjectInstructions: []string{"always run tests", "use gofmt"},
	})
	if !strings.Contains(out.Messages[0].Content, "always run tests") {
		t.Fatal("project instructions missing")
	}
}

func TestFilesIncludedAndCapped(t *testing.T) {
	c := NewComposer(Limits{})
	big := strings.Repeat("x", 40_000)
	out, _ := c.Compose(Input{
		Spec:  task.Spec{Prompt: "look at these"},
		Files: []FileRef{{Path: "a.txt", Content: big}, {Path: "b.txt", Content: "small"}},
	})
	joined := out.Messages[1].Content
	if !strings.Contains(joined, "a.txt") || !strings.Contains(joined, "b.txt") {
		t.Fatal("files missing from user message")
	}
	if !strings.Contains(joined, "truncated") {
		t.Fatal("large file should be truncated")
	}
}

func TestHistoryCondensedAtLimit(t *testing.T) {
	c := NewComposer(Limits{CondenseAt: 200, MaxTokens: 1000})
	var history []Message
	for i := 0; i < 20; i++ {
		history = append(history, Message{Role: "assistant", Content: strings.Repeat("word ", 50)})
	}
	out, err := c.Compose(Input{
		Spec:    task.Spec{Prompt: "continue"},
		History: history,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !out.Condensed {
		t.Fatal("expected condensation")
	}
	if out.Dropped == 0 {
		t.Fatal("expected dropped messages")
	}
	if out.Tokens > 1000 {
		t.Fatalf("tokens %d exceed cap", out.Tokens)
	}
}

func TestEstimate(t *testing.T) {
	if Estimate("") != 0 {
		t.Fatal("empty estimate")
	}
	if Estimate("hello world") < 1 {
		t.Fatal("non-empty estimate")
	}
	// 4000 runes ≈ 1000 tokens.
	if Estimate(strings.Repeat("a", 4000)) != 1000 {
		t.Fatalf("estimate = %d", Estimate(strings.Repeat("a", 4000)))
	}
}

func TestSummarize(t *testing.T) {
	s := Summarize([]Message{
		{Role: "assistant", Content: "I read the parser and fixed the bug in lexer.go."},
		{Role: "assistant", Content: strings.Repeat("long ", 500)},
	}, 400)
	if !strings.Contains(s, "lexer.go") {
		t.Fatalf("summary missing key content: %q", s)
	}
	if len(s) > 600 {
		t.Fatalf("summary too long: %d", len(s))
	}
}
