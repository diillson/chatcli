/*
 * ChatCLI - Long-term memory tests: IDF-weighted keyword relevance.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package memory

import (
	"testing"

	"go.uber.org/zap"
)

func TestComputeRelevance_RareKeywordsWeighMore(t *testing.T) {
	fi := NewFactIndex(t.TempDir(), DefaultConfig(), zap.NewNop())
	// "deploy" is in every fact; "kubernetes" names exactly one.
	fi.AddFact("deploy pipeline uses kubernetes", "architecture", nil)
	fi.AddFact("deploy happens on fridays", "project", nil)
	fi.AddFact("deploy notes live in the wiki", "project", nil)
	fi.AddFact("deploy approvals need two reviewers", "pattern", nil)
	fi.AddFact("the deploy bot posts to slack", "pattern", nil)
	fi.mu.RLock()
	w := fi.keywordWeightsLocked([]string{"deploy", "kubernetes"})
	fi.mu.RUnlock()
	if !(w[1] > w[0]) || w[0] < 0.5 || w[1] > 2 {
		t.Fatalf("rare keyword must outweigh the common one within the clamp: %v", w)
	}
	hits := fi.Search([]string{"deploy", "kubernetes"})
	if len(hits) == 0 || hits[0].Content != "deploy pipeline uses kubernetes" {
		t.Fatalf("the fact matching the rare term must rank first: %v", hits)
	}
	// Few facts: no weighting (all ones), so tiny indexes behave as before.
	small := NewFactIndex(t.TempDir(), DefaultConfig(), zap.NewNop())
	small.AddFact("only one", "general", nil)
	small.mu.RLock()
	ws := small.keywordWeightsLocked([]string{"only", "zzz"})
	small.mu.RUnlock()
	if ws[0] != 1 || ws[1] != 1 {
		t.Fatalf("small index weights must be neutral: %v", ws)
	}
	// The df cache follows mutations.
	fi.AddFact("kubernetes again", "general", nil)
	fi.mu.RLock()
	df := fi.docFreqLocked()
	fi.mu.RUnlock()
	if df["kubernetes"] != 2 {
		t.Fatalf("df cache must refresh after a mutation: %d", df["kubernetes"])
	}
}
