/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package client

import (
	"strings"
	"sync"

	"github.com/diillson/chatcli/models"
)

// Reasoning blocks bound to their conversation prefix.
//
// From Claude Fable 5.1 on, a `thinking` block is valid only against the
// system prompt, the tools and the messages that preceded it. Fable 5.1 and
// Opus 5.5 enforce that for accounts created from Aug 31 2026; Fable 5.2
// enforces it for every account (platform.claude.com/docs/en/models/
// fable-5-2/migration-guide). A request that replays a block after any of
// those changed — a compacted history, a system prompt that gained a
// section, a tool that was added — is answered
//
//	400 messages.N.content.M: Invalid `signature` in `thinking` block. The
//	block is bound to a different conversation. Remove the block, or ...
//
// and resending the same body never clears it. This client edits the
// prefix by design (compaction, context that is attached and detached), so
// the answer is the one the error itself names: remove the blocks. The
// reasoning is lost for the turns it covered; the conversation goes on.

// thinkingBindingMarker is the stable part of the rejection. It is matched
// as text because the error reaches callers through three transports (the
// first-party API, Bedrock InvokeModel and the Bedrock Messages endpoint),
// each wrapping the same upstream message in its own error type.
const thinkingBindingMarker = "bound to a different conversation"

// IsThinkingBindingError reports whether err is the provider refusing a
// replayed reasoning block because the conversation before it changed.
func IsThinkingBindingError(err error) bool {
	return err != nil && strings.Contains(err.Error(), thinkingBindingMarker)
}

// StaleThinking remembers the reasoning blocks a provider refused to take
// back, so that they are left out of every later request of the same
// client. Leaving them out consistently matters: blocks produced after the
// refusal are bound to a prefix that no longer carries the refused ones,
// and putting those back would invalidate the new blocks in turn.
//
// The zero value is ready to use; a nil *StaleThinking remembers nothing,
// so a client built without one simply never resends.
type StaleThinking struct {
	mu   sync.RWMutex
	seen map[string]struct{}
}

// thinkingBlockKey identifies a block by the opaque part the provider
// issued: the signature of a thinking block, the payload of a redacted one.
func thinkingBlockKey(b models.ThinkingBlock) string {
	if b.Signature != "" {
		return b.Signature
	}
	return b.Data
}

// Filter returns history without the blocks marked stale. The input is
// never modified; when nothing is stale it is returned as is.
func (s *StaleThinking) Filter(history []models.Message) []models.Message {
	if s == nil {
		return history
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.seen) == 0 {
		return history
	}
	out := history
	copied := false
	for i, msg := range history {
		if len(msg.Thinking) == 0 {
			continue
		}
		kept := make([]models.ThinkingBlock, 0, len(msg.Thinking))
		for _, b := range msg.Thinking {
			if _, stale := s.seen[thinkingBlockKey(b)]; !stale {
				kept = append(kept, b)
			}
		}
		if len(kept) == len(msg.Thinking) {
			continue
		}
		if !copied {
			out = append([]models.Message(nil), history...)
			copied = true
		}
		out[i].Thinking = kept
	}
	return out
}

// MarkAll marks every reasoning block in history as stale and reports
// whether any of them was not marked before — the only case in which a
// resend differs from the request that was just refused.
func (s *StaleThinking) MarkAll(history []models.Message) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	added := false
	for _, msg := range history {
		for _, b := range msg.Thinking {
			key := thinkingBlockKey(b)
			if key == "" {
				continue
			}
			if _, ok := s.seen[key]; ok {
				continue
			}
			if s.seen == nil {
				s.seen = make(map[string]struct{})
			}
			s.seen[key] = struct{}{}
			added = true
		}
	}
	return added
}

// Heal runs send over history without the blocks already known to be stale
// and, when the provider refuses a block for being bound to a different
// conversation, marks every block of the history stale and sends once
// more. Any other error, and a second refusal, come back as they are.
// dropped tells the caller a resend happened, so it can log it. send keeps
// its own result: it is called again only after a refusal, which produced
// none.
func (s *StaleThinking) Heal(history []models.Message, send func(history []models.Message) error) (dropped bool, err error) {
	err = send(s.Filter(history))
	if !IsThinkingBindingError(err) || !s.MarkAll(history) {
		return false, err
	}
	return true, send(s.Filter(history))
}
