/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Global prompt budget for the system prefix.
 *
 * The compactor budgets the CONVERSATION against the model window; the
 * prefix in front of it (mode card, attached contexts, knowledge digests,
 * skills, tool catalog, recall blocks) was assembled section by section
 * with local caps only, so a session with several attachments and a few
 * fat skills could fill the window before the first user message — and
 * the compactor would then starve the history to make room. This budget
 * sums the prefix as it is assembled and, when the prefix alone would
 * cross its share of the window, degrades sections in a declared order:
 *
 *   1. skills — bodies fold into read-on-demand pointers (the model still
 *      learns which skills apply and where to read them);
 *   2. knowledge digests — index cards shrink to their compact form;
 *   3. attached contexts — whole-content attachments fold into an index
 *      card that names the files and how to pull them (@context,
 *      /context attach --rag).
 *
 * Every degradation is recorded on the prompt breakdown so /context
 * status shows what was folded and why. Nothing is dropped silently, and
 * a session that fits keeps exactly the prompt it always had.
 */
package cli

import (
	"github.com/diillson/chatcli/llm/catalog"
)

const (
	// prefixMaxShare is the share of the window the prefix may occupy
	// before sections degrade; the rest stays for the conversation and
	// the reply.
	prefixMaxShare = 0.50
	// prefixFloorChars keeps small-window models usable: below this the
	// budget would fold everything on every turn.
	prefixFloorChars = 24000
	// prefixVolatileReserve is kept free for the volatile tail (workspace
	// memory, retrieval, recall, dynamic line) when sizing the stable
	// sections that are built first.
	prefixVolatileReserve = 6000
)

// prefixBudget tracks how much of the prefix budget the assembled
// sections have spent and which ones degraded.
type prefixBudget struct {
	Window   int
	MaxChars int
	used     int
	degraded []string
}

// newPrefixBudget sizes the budget for a provider/model from the catalog
// window and the learned chars-per-token ratio.
func (cli *ChatCLI) newPrefixBudget(provider, model string) *prefixBudget {
	window := catalog.GetContextWindow(provider, model)
	cpt := cli.frozenPrefixRatio(provider, model)
	limit := int(float64(window) * prefixMaxShare * cpt)
	if limit < prefixFloorChars {
		limit = prefixFloorChars
	}
	return &prefixBudget{Window: window, MaxChars: limit}
}

// frozenPrefixRatio returns the chars-per-token ratio the prefix budget
// uses for provider/model, fixed at first use for the session: the learned
// ratio keeps moving by EMA on every request, and a budget that follows it
// folds and unfolds cached sections (attachments, digests, pinned skills)
// between turns — a prefix rebuild each time. A model switch keys a fresh
// ratio.
func (cli *ChatCLI) frozenPrefixRatio(provider, model string) float64 {
	if cli == nil {
		return defaultCharsPerToken
	}
	key := calibrationKey(provider, model)
	cli.prefixRatiosMu.Lock()
	defer cli.prefixRatiosMu.Unlock()
	if r, ok := cli.prefixRatios[key]; ok && r > 0 {
		return r
	}
	cpt, _ := cli.calibrator().CharsPerToken(provider, model)
	if cpt <= 0 {
		cpt = defaultCharsPerToken
	}
	if cli.prefixRatios == nil {
		cli.prefixRatios = map[string]float64{}
	}
	cli.prefixRatios[key] = cpt
	return cpt
}

// spend records chars already placed in the prefix.
func (b *prefixBudget) spend(n int) {
	if b != nil && n > 0 {
		b.used += n
	}
}

// remaining is what the prefix may still take, keeping the volatile
// reserve free; never negative.
func (b *prefixBudget) remaining() int {
	if b == nil {
		return int(^uint(0) >> 1)
	}
	r := b.MaxChars - b.used - prefixVolatileReserve
	if r < 0 {
		return 0
	}
	return r
}

// allow caps a section's own budget by what remains.
func (b *prefixBudget) allow(own int) int {
	if b == nil {
		return own
	}
	if r := b.remaining(); own <= 0 || own > r {
		return r
	}
	return own
}

// noteDegraded records that a section was folded.
func (b *prefixBudget) noteDegraded(section string) {
	if b == nil || section == "" {
		return
	}
	for _, d := range b.degraded {
		if d == section {
			return
		}
	}
	b.degraded = append(b.degraded, section)
}

// Degraded lists the folded sections in the order they folded.
func (b *prefixBudget) Degraded() []string {
	if b == nil {
		return nil
	}
	return append([]string(nil), b.degraded...)
}
