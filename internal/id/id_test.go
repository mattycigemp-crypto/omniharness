package id

import (
	"strings"
	"sync"
	"testing"
)

func TestNewShape(t *testing.T) {
	// IDs end up in file names, URLs and SQLite keys, so the shape is part of
	// the contract, not an implementation detail.
	const hexDigits = "0123456789abcdef"
	for i := 0; i < 100; i++ {
		got := New()
		if len(got) != 32 {
			t.Fatalf("New() = %q, length %d, want 32", got, len(got))
		}
		if strings.Trim(got, hexDigits) != "" {
			t.Fatalf("New() = %q, want lowercase hex only", got)
		}
	}
}

func TestNewIsUnique(t *testing.T) {
	// 16 bytes of entropy: a collision in this many draws would mean the
	// source is not random rather than that we got unlucky.
	const draws = 50_000
	seen := make(map[string]struct{}, draws)
	for i := 0; i < draws; i++ {
		got := New()
		if _, dup := seen[got]; dup {
			t.Fatalf("New() repeated %q within %d draws", got, draws)
		}
		seen[got] = struct{}{}
	}
}

func TestNewVariesAcrossTheWholeString(t *testing.T) {
	// A truncated or partly zeroed buffer still passes a length check, so
	// look for variation at every position.
	const draws = 512
	seenAt := make([]map[byte]struct{}, 32)
	for i := range seenAt {
		seenAt[i] = map[byte]struct{}{}
	}
	for i := 0; i < draws; i++ {
		got := New()
		for pos := 0; pos < 32; pos++ {
			seenAt[pos][got[pos]] = struct{}{}
		}
	}
	for pos, values := range seenAt {
		if len(values) < 8 {
			t.Errorf("position %d took only %d distinct values across %d ids", pos, len(values), draws)
		}
	}
}

func TestNewIsSafeUnderConcurrency(t *testing.T) {
	// Every id in the system is minted from goroutines running in parallel —
	// agents, event publishers, the orchestrator. Run under -race.
	const (
		workers = 16
		each    = 500
	)
	var wg sync.WaitGroup
	results := make(chan string, workers*each)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < each; i++ {
				results <- New()
			}
		}()
	}
	wg.Wait()
	close(results)

	seen := make(map[string]struct{}, workers*each)
	for got := range results {
		if len(got) != 32 {
			t.Fatalf("concurrent New() produced %q", got)
		}
		if _, dup := seen[got]; dup {
			t.Fatalf("concurrent New() repeated %q", got)
		}
		seen[got] = struct{}{}
	}
	if len(seen) != workers*each {
		t.Fatalf("got %d ids, want %d", len(seen), workers*each)
	}
}
