/*
 * ChatCLI - HyDE Phase 3 tests.
 */
package memory

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestHyDEAugmenter_NilReturnsHints(t *testing.T) {
	var aug *HyDEAugmenter
	got := aug.Augment(context.Background(), "q", []string{"a", "b"})
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("nil augmenter must return original hints; got %v", got)
	}
}

func TestHyDEAugmenter_DisabledReturnsHints(t *testing.T) {
	aug := NewHyDEAugmenter(HyDEConfig{Enabled: false}, func(_ context.Context, _ string) (string, error) {
		return "ignored", nil
	}, nil)
	got := aug.Augment(context.Background(), "q", []string{"a"})
	if len(got) != 1 || got[0] != "a" {
		t.Fatalf("disabled augmenter must return original hints; got %v", got)
	}
}

func TestHyDEAugmenter_LLMErrorFallsBack(t *testing.T) {
	aug := NewHyDEAugmenter(HyDEConfig{Enabled: true, NumKeywords: 5}, func(_ context.Context, _ string) (string, error) {
		return "", errors.New("network down")
	}, nil)
	got := aug.Augment(context.Background(), "q", []string{"orig"})
	if len(got) != 1 || got[0] != "orig" {
		t.Fatalf("LLM error must fall back to original hints; got %v", got)
	}
}

func TestHyDEAugmenter_EmptyQueryFallsBack(t *testing.T) {
	called := false
	aug := NewHyDEAugmenter(HyDEConfig{Enabled: true}, func(_ context.Context, _ string) (string, error) {
		called = true
		return "ignored", nil
	}, nil)
	got := aug.Augment(context.Background(), "  ", []string{"orig"})
	if called {
		t.Fatalf("empty query must skip LLM call")
	}
	if len(got) != 1 || got[0] != "orig" {
		t.Fatalf("empty query must return original hints; got %v", got)
	}
}

func TestHyDEAugmenter_AugmentsWithHypothesisKeywords(t *testing.T) {
	aug := NewHyDEAugmenter(HyDEConfig{Enabled: true, NumKeywords: 5}, func(_ context.Context, prompt string) (string, error) {
		// Return a hypothesis full of distinct technical nouns. ExtractKeywords
		// drops stop words and short tokens.
		if !strings.Contains(prompt, "Question: how do I write a goroutine") {
			t.Errorf("prompt missing question: %q", prompt)
		}
		return "Goroutines are lightweight execution units in golang scheduler. Use channels for synchronization.", nil
	}, nil)
	got := aug.Augment(context.Background(), "how do I write a goroutine", []string{"orig"})
	if len(got) <= 1 {
		t.Fatalf("augmented hints must include hypothesis keywords; got %v", got)
	}
	// Original hint preserved as first entry.
	if got[0] != "orig" {
		t.Fatalf("first hint must remain unchanged; got %v", got)
	}
}

func TestHyDEAugmenter_RespectsNumKeywordsCap(t *testing.T) {
	aug := NewHyDEAugmenter(HyDEConfig{Enabled: true, NumKeywords: 2}, func(_ context.Context, _ string) (string, error) {
		return "alpha bravo charlie delta echo foxtrot golf hotel", nil
	}, nil)
	got := aug.Augment(context.Background(), "q", []string{"orig"})
	// 1 original + at most 2 hypothesis keywords
	if len(got) > 3 {
		t.Errorf("NumKeywords cap not honoured; got %v", got)
	}
}

func TestMergeUniqueLowercase_DeduplicatesCaseInsensitive(t *testing.T) {
	got := mergeUniqueLowercase([]string{"Foo", "Bar"}, []string{"foo", "BAZ"})
	if len(got) != 3 {
		t.Errorf("expected 3 unique keys; got %v", got)
	}
}

func TestMergeUniqueLowercase_SkipsEmpty(t *testing.T) {
	got := mergeUniqueLowercase([]string{"", " "}, []string{"x", "  "})
	if len(got) != 1 || got[0] != "x" {
		t.Errorf("empty/whitespace strings must be skipped; got %v", got)
	}
}
