/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package plugins

import (
	"fmt"
	"strings"
)

// TruncationAware is implemented by plugins that want fine-grained
// control over how their oversize output is trimmed before going into
// the LLM context. The global default (30 000 chars) made sense as a
// blanket safety net but punishes plugins like @search and @tree that
// legitimately produce large structured output the model needs in
// full, while letting @webfetch (which IS in turn handled by its own
// auto-save path) still hit a different threshold.
//
// MaxResultChars returns the soft cap. Output longer than this is
// truncated using TruncateForLLM. A value <= 0 means "use the global
// default"; the orchestrator decides what that default is at runtime.
type TruncationAware interface {
	MaxResultChars() int
}

// DefaultMaxResultChars is the soft cap applied to every plugin that
// does not implement TruncationAware. Matches the historical
// hard-coded 30 000 in agent_mode.go.
const DefaultMaxResultChars = 30_000

// TruncationPreviewSize is the byte budget for the prefix portion of
// a truncated payload. The shape is "<prefix>\n\n... [TRUNCATED] ...\n\n<suffix>"
// where prefix is up to 5/6 of the budget and suffix is 1/6. The
// distribution is empirical: heads of large outputs usually contain
// the most useful information for the LLM (function signatures,
// schema, file listing) while the tail catches stack traces or
// summary lines.
const (
	TruncationPreviewSize = 5000
	TruncationSuffixSize  = 1000
)

// EffectiveMaxResultChars resolves the active cap for one tool call.
// Plugins that implement TruncationAware with a positive value win;
// otherwise the global default applies.
func EffectiveMaxResultChars(plugin Plugin) int {
	if plugin == nil {
		return DefaultMaxResultChars
	}
	if ta, ok := plugin.(TruncationAware); ok {
		if n := ta.MaxResultChars(); n > 0 {
			return n
		}
	}
	return DefaultMaxResultChars
}

// TruncateForLLM trims output to fit within the supplied cap. Returns
// the original string when it fits; otherwise a head/tail concatenation
// with an explicit "[TRUNCATED]" marker that names the dropped char
// count so the model knows it's looking at a redacted view.
//
// The trim is intentionally not provider-agnostic for token math —
// chatcli's LLM clients each have their own token budget logic. This
// helper is for byte-level safety only.
func TruncateForLLM(s string, maxChars int) string {
	if maxChars <= 0 {
		maxChars = DefaultMaxResultChars
	}
	if len(s) <= maxChars {
		return s
	}
	preview := TruncationPreviewSize
	suffix := TruncationSuffixSize
	if preview+suffix >= maxChars {
		// Cap too small for both halves; cut at maxChars and append
		// a marker.
		return s[:maxChars] + "\n\n... [TRUNCATED]"
	}
	head := s[:preview]
	tail := s[len(s)-suffix:]
	dropped := len(s) - preview - suffix
	var b strings.Builder
	b.Grow(preview + suffix + 64)
	b.WriteString(head)
	b.WriteString("\n\n... [TRUNCATED ")
	fmt.Fprintf(&b, "%d chars omitted, %d kept", dropped, preview+suffix)
	b.WriteString("] ...\n\n")
	b.WriteString(tail)
	return b.String()
}
