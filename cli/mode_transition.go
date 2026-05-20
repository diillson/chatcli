/*
 * ChatCLI - Mode transition cleanup.
 *
 * Single source of truth for the rule "exactly one ACTIVE MODE marker
 * may exist in the system slot at any time". When the user moves
 * between /chat, /agent, and /coder mid-session, the per-mode entry
 * points used to leave their own system prompt behind in cli.history.
 * Without cleanup, the next mode's prompt assembler would ship BOTH
 * prompts to the LLM (the current one in slot 0 and the stale one as
 * a mid-history system message), and the model would receive
 * contradictory format rules — "don't emit tool_call" alongside "you
 * MUST emit tool_call". This file owns the filtering that prevents
 * that drift.
 */
package cli

import (
	"regexp"

	"github.com/diillson/chatcli/models"
)

// Mode names match the canonical `[ACTIVE MODE: <name>]` markers we
// embed at the top of every system prompt. Anything that doesn't
// match is treated as a non-mode system message and left alone.
const (
	ModeChat  = "chat"
	ModeAgent = "/agent"
	ModeCoder = "/coder"
)

// modeMarkerRe extracts the mode name from the canonical marker we
// embed at the top of every mode-aware system prompt. Captures the
// mode name verbatim so callers can compare to the constants above.
//
// We tolerate optional whitespace inside the brackets but not other
// noise, so a typo like `[ ACTIVE MODE :chat ]` simply won't match
// and the message is treated as "no mode declared" (kept untouched).
var modeMarkerRe = regexp.MustCompile(`\[ACTIVE MODE:\s*([^\]\s]+)\s*\]`)

// modeOfSystemMessage returns the canonical mode name carried by the
// system message, or "" when the message either isn't a system role
// or carries no marker. Used by purgeStaleModeSystems to decide
// whether each entry should survive a mode transition.
func modeOfSystemMessage(m models.Message) string {
	if m.Role != "system" {
		return ""
	}
	if match := modeMarkerRe.FindStringSubmatch(m.Content); len(match) == 2 {
		return match[1]
	}
	// SystemParts may carry the marker too when the flat Content is
	// empty (Anthropic-style block emission). Walk the parts as a
	// fallback so we don't miss a marker that lives only in the
	// structured side of the message.
	for _, p := range m.SystemParts {
		if match := modeMarkerRe.FindStringSubmatch(p.Text); len(match) == 2 {
			return match[1]
		}
	}
	return ""
}

// purgeStaleModeSystems removes every system message whose mode
// marker does NOT match `currentMode`. Messages without a marker
// (e.g. context-injection blocks added by /context attach) are kept
// untouched so we don't accidentally drop user-attached context.
//
// Returns a NEW slice; the input is never mutated. The cost is one
// allocation per call, which is amortized against the LLM round-trip
// that follows — orders of magnitude more expensive.
func purgeStaleModeSystems(history []models.Message, currentMode string) []models.Message {
	out := make([]models.Message, 0, len(history))
	for _, msg := range history {
		mode := modeOfSystemMessage(msg)
		if mode != "" && mode != currentMode {
			// Stale mode marker — drop the entire system message.
			// The fresh system prompt for `currentMode` is injected
			// by the caller right before the LLM call, so dropping
			// the stale one never leaves the LLM without guidance.
			continue
		}
		out = append(out, msg)
	}
	return out
}
