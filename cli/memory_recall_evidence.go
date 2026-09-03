/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Evidence-based reinforcement of recalled facts.
 *
 * Auto-recall injects a few long-term facts into every turn. Bumping their
 * access counters at injection time would reward whatever the ranker
 * already favors (rich get richer) regardless of whether the model used
 * the fact. Instead, the facts shown are remembered per turn and only
 * those the assistant's reply actually drew on — enough of the fact's
 * own significant terms appear in the answer — are reinforced. Nothing is
 * demoted: a fact the model ignored simply keeps its score.
 */
package cli

import (
	"strings"
	"sync"

	"github.com/diillson/chatcli/cli/workspace/memory"
)

const (
	// recallEvidenceMinTerms is the least number of a fact's significant
	// terms that must appear in the reply (or all of them for facts with
	// fewer terms) to count as used.
	recallEvidenceMinTerms = 2
	// recallEvidenceMinShare is the share of a fact's terms that must
	// appear for long facts (so a 12-term fact needs more than 2 hits).
	recallEvidenceMinShare = 0.34
)

// recalledFact is one injected fact and the terms that identify it.
type recalledFact struct {
	id    string
	terms []string
}

// recallEvidence holds the facts injected for the turn in flight.
type recallEvidence struct {
	mu    sync.Mutex
	facts []recalledFact
}

// noteRecalledFacts records the facts an auto-recall block injected for
// the current turn, replacing the previous turn's set.
func (cli *ChatCLI) noteRecalledFacts(facts []*memory.Fact) {
	if cli == nil {
		return
	}
	set := make([]recalledFact, 0, len(facts))
	for _, f := range facts {
		if f == nil || f.ID == "" {
			continue
		}
		terms := memory.ExtractKeywords([]string{f.Content})
		if len(terms) == 0 {
			continue
		}
		set = append(set, recalledFact{id: f.ID, terms: terms})
	}
	cli.recalled.mu.Lock()
	cli.recalled.facts = set
	cli.recalled.mu.Unlock()
}

// reinforceRecalledFacts marks as accessed the injected facts the reply
// evidently used, then clears the turn's set. Returns the ids reinforced.
func (cli *ChatCLI) reinforceRecalledFacts(reply string) []string {
	if cli == nil {
		return nil
	}
	cli.recalled.mu.Lock()
	facts := cli.recalled.facts
	cli.recalled.facts = nil
	cli.recalled.mu.Unlock()
	if len(facts) == 0 || strings.TrimSpace(reply) == "" || cli.memoryStore == nil {
		return nil
	}
	hay := strings.ToLower(reply)
	var used []string
	for _, f := range facts {
		if factEvidenced(f.terms, hay) {
			used = append(used, f.id)
		}
	}
	if len(used) == 0 {
		return nil
	}
	if mgr := cli.memoryStore.Manager(); mgr != nil && mgr.Facts != nil {
		mgr.Facts.MarkAccessed(used)
	}
	return used
}

// factEvidenced reports whether enough of terms appear in hay
// (case-insensitive).
func factEvidenced(terms []string, hay string) bool {
	if len(terms) == 0 {
		return false
	}
	hay = strings.ToLower(hay)
	hits := 0
	for _, t := range terms {
		if t != "" && strings.Contains(hay, strings.ToLower(t)) {
			hits++
		}
	}
	need := recallEvidenceMinTerms
	if len(terms) < need {
		need = len(terms)
	}
	if share := int(float64(len(terms))*recallEvidenceMinShare + 0.999); share > need {
		need = share
	}
	return hits >= need
}
