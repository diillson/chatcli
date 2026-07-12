/*
 * ChatCLI - Unified response envelope rendering
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Renders the assistant's final reply in the "sóbrio" treatment: a
 * bilateral titled rule opens the reply (model on the left, latency and
 * tokens on the right), the body sits on a two-space indent wrapped to the
 * live terminal width with ANSI preserved, and a single dim telemetry line
 * closes it. The envelope stays the single source of truth for chat, coder
 * and agent modes: callers supply pre-formatted labels and a body; an
 * optional typewriter effect plays the body progressively.
 */
package agent

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/diillson/chatcli/ui/kit"
)

// NOTE: the runewidth normalization (StrictEmojiNeutral=false) that used to
// live in an init() here moved to ui/kit, which this package imports — the
// emoji-width guarantee is unchanged (kit has a guard test for it).

// TerminalWidth reports the live terminal width in columns. Delegates to
// kit.TermWidth — the single width helper (fallback 100 when stdout is not
// a TTY).
func TerminalWidth() int {
	return kit.TermWidth()
}

// EnvelopeWidth returns the width to use for a response envelope on the
// current terminal. Delegates to kit.ContentWidth: right-edge margin so
// native scrollbars never clip the border, clamped to a minimum so the box
// never collapses on tiny terminals; no upper cap (full-screen terminals
// use their full width — a direct user preference).
func EnvelopeWidth() int {
	return kit.ContentWidth()
}

// ResponseEnvelopeOptions configures the unified reply rendering. All
// label fields are PRE-FORMATTED: callers own colorization and any
// leading/trailing spaces they want carved out of the dash fill.
// Empty fields are omitted (no extra space reserved).
type ResponseEnvelopeOptions struct {
	// HeaderLeft is the visible label on the top border's left side.
	// Conventionally the icon + title (e.g. " 💬 RESPOSTA ") in color.
	HeaderLeft string

	// HeaderRight is the visible label on the top border's right side.
	// Conventionally the metrics block (e.g. " 1.4s · 312↑ 1.8k↓ ").
	// Pass an empty string to draw only the left label.
	HeaderRight string

	// FooterLeft and FooterRight compose the dim telemetry line that
	// closes the reply. Most callers leave them empty (the body's
	// terminal punctuation closes the thought).
	FooterLeft  string
	FooterRight string

	// Body is the message content to render inside the box. Typically
	// glamour-rendered markdown (ANSI escapes preserved); the envelope
	// wraps it to the resolved inner width.
	Body string

	// Color is kept for API stability; the sóbrio treatment draws dim
	// chrome and expects labels to arrive pre-colored.
	Color string

	// Typewriter enables progressive rune-by-rune painting of the body
	// for the "alive" reply feel. ANSI escapes flush instantly so
	// colors never pause the eye.
	Typewriter bool

	// TypewriterDelay overrides the per-rune delay. Zero uses the
	// default of 2ms — fast enough for long replies, slow enough to
	// register as animation. Set to a positive value to slow down or
	// to a negative value (caller-side check) to disable.
	TypewriterDelay time.Duration

	// Width pins the card width in columns. Zero asks the envelope to
	// pick EnvelopeWidth() automatically — the right choice for almost
	// every caller. Tests and special UIs (split-pane reports) can
	// override this.
	Width int
}

// RenderResponseEnvelope paints the assistant's reply: titled rule with
// the bilateral labels, indented body (wrapStructured preserves the
// indentation of glamour-rendered YAML/JSON/code), dim telemetry footer.
func (r *UIRenderer) RenderResponseEnvelope(opts ResponseEnvelopeOptions) {
	maxWidth := opts.Width
	if maxWidth <= 0 {
		maxWidth = EnvelopeWidth()
	}
	if maxWidth < 24 {
		maxWidth = 24
	}

	// "Sóbrio" treatment: no closed box. A bilateral titled rule opens the
	// reply, the body sits on a two-space indent, and telemetry closes it
	// as a single dim line. The Color option is intentionally unused for
	// chrome — rules are dim, labels arrive pre-colored from the caller.
	const indent = "  "
	maxInner := maxWidth - len(indent)
	if maxInner < 16 {
		maxInner = 16
	}

	body := strings.Trim(opts.Body, "\n\r")
	if body == "" {
		body = " "
	}
	// wrapStructured (não wrapText) preserva a indentação de YAML/JSON/código
	// renderizado pelo glamour.
	wrapped := trimBlankBorderRows(wrapStructured(body, maxInner))
	var bodyRendered strings.Builder
	for i, ln := range wrapped {
		if i > 0 {
			bodyRendered.WriteString("\n")
		}
		if lipgloss.Width(ln) > 0 {
			bodyRendered.WriteString(indent)
			bodyRendered.WriteString(ln)
		}
	}

	topLine := kit.RuleHeader(opts.HeaderLeft, opts.HeaderRight, maxWidth)

	footer := strings.TrimSpace(opts.FooterLeft)
	if fr := strings.TrimSpace(opts.FooterRight); fr != "" {
		if footer != "" {
			footer += " "
		}
		footer += fr
	}

	delay := opts.TypewriterDelay
	if delay == 0 {
		delay = defaultDelay
	}

	fmt.Println()
	fmt.Println(topLine)
	fmt.Println()
	if opts.Typewriter {
		PaceText(bodyRendered.String(), delay)
		fmt.Println()
	} else {
		fmt.Println(bodyRendered.String())
	}
	if footer != "" {
		fmt.Println()
		fmt.Println(indent + footer)
	}
}
