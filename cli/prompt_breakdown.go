/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Prompt breakdown: what the last assembled system prompt was made of.
 *
 * Seven different injectors contribute to a turn's system prompt (mode
 * banner, attached contexts, knowledge digests, skills, MCP catalog,
 * workspace memory, recall blocks, dynamic context). Until now nothing
 * recorded their sizes, so "what is in my context right now" had no
 * answer. Both assembly paths — chat (assembleChatSystemPrompt) and
 * agent/coder (buildAgentSystemMessage) — now record a labeled section
 * list here, and /context status renders it with token estimates.
 */
package cli

import (
	"sync"
	"time"

	"github.com/diillson/chatcli/models"
)

// promptSection is one labeled block of the assembled system prompt.
type promptSection struct {
	Name   string // i18n key suffix under context.status.section.*
	Chars  int
	Cached bool // sits in the cacheable stable prefix
}

// promptBreakdown is the snapshot of the last assembled system prompt.
type promptBreakdown struct {
	Mode     string // "chat", "agent" or "coder"
	Sections []promptSection
	At       time.Time
}

// TotalChars sums every section.
func (b *promptBreakdown) TotalChars() int {
	if b == nil {
		return 0
	}
	n := 0
	for _, s := range b.Sections {
		n += s.Chars
	}
	return n
}

// CachedChars sums the sections that live in the cacheable prefix.
func (b *promptBreakdown) CachedChars() int {
	if b == nil {
		return 0
	}
	n := 0
	for _, s := range b.Sections {
		if s.Cached {
			n += s.Chars
		}
	}
	return n
}

// promptBreakdownStore keeps the latest snapshot per process; the chat
// pipeline and the agent loop write it, /context status reads it.
type promptBreakdownStore struct {
	mu   sync.RWMutex
	last *promptBreakdown
}

func (s *promptBreakdownStore) record(mode string, sections []promptSection) {
	kept := make([]promptSection, 0, len(sections))
	for _, sec := range sections {
		if sec.Chars > 0 {
			kept = append(kept, sec)
		}
	}
	s.mu.Lock()
	s.last = &promptBreakdown{Mode: mode, Sections: kept, At: time.Now()}
	s.mu.Unlock()
}

func (s *promptBreakdownStore) latest() *promptBreakdown {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.last
}

// add appends blocks to the chat assembly under a section label, so the
// breakdown records what each part contributed without changing the order
// or content of what the model receives.
func (a *chatSystemAssembly) add(label string, blocks ...models.ContentBlock) {
	for _, b := range blocks {
		a.parts = append(a.parts, b)
		a.sections = append(a.sections, promptSection{Name: label, Chars: len(b.Text), Cached: b.CacheControl != nil})
	}
}
