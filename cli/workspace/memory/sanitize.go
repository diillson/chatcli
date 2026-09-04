/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Fact content hygiene shared by the deterministic paths (/memory remember,
 * import) — the background extractor redacts before it distills, these
 * two used to bypass it.
 */
package memory

import (
	"strings"
	"sync/atomic"
)

// maxFactContentChars bounds one stored fact; longer text is not a fact.
const maxFactContentChars = 2000

// maxFactTags bounds the tag list an import may carry.
const maxFactTags = 16

// redactor is the secret redaction the owning process installs (the CLI's
// LLM-path redactor); nil leaves content unchanged.
var redactor atomic.Pointer[func(string) string]

// SetRedactor installs the secret redaction applied to every fact stored
// through RememberFact and ImportFacts.
func SetRedactor(fn func(string) string) {
	if fn == nil {
		redactor.Store(nil)
		return
	}
	redactor.Store(&fn)
}

// SanitizeFactContent collapses content to one line, bounds it and masks
// secrets. Returns "" for content that has nothing left.
func SanitizeFactContent(content string) string {
	c := strings.Join(strings.Fields(content), " ")
	if len(c) > maxFactContentChars {
		c = c[:maxFactContentChars]
		for len(c) > 0 && c[len(c)-1]&0xC0 == 0x80 { // never split a rune
			c = c[:len(c)-1]
		}
		c = strings.TrimSpace(c) + "…"
	}
	if fn := redactor.Load(); fn != nil {
		c = (*fn)(c)
	}
	return strings.TrimSpace(c)
}

// clampConfidence keeps confidence in (0,1]; 0 stays 0 (unset/default).
func clampConfidence(c float64) float64 {
	switch {
	case c < 0:
		return 0
	case c > 1:
		return 1
	}
	return c
}
