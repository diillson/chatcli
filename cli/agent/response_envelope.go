/*
 * ChatCLI - Unified response envelope rendering
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Renders the assistant's final reply inside a bordered, responsive,
 * ANSI-aware lipgloss box. The envelope is the single source of truth
 * for chat, coder and agent modes: each caller supplies bilateral
 * header/footer labels and a body, and the renderer guarantees:
 *
 *   - The card width follows the live terminal width (no hardcoded cols).
 *   - The body is wrapped to the box inner width preserving ANSI escapes.
 *   - Top, sides and bottom borders are drawn by lipgloss so widths agree.
 *   - Emoji widths are normalized so the right border never drifts.
 *   - An optional typewriter effect plays the body progressively.
 *
 * Why a dedicated file instead of stuffing this into ui_renderer.go:
 * the timeline-card path (RenderTimelineEvent) is legacy and biased
 * toward icon+title headers; chat needs a bilateral header (model on
 * the left, latency/tokens on the right). Sharing the math without
 * blurring the two APIs keeps each call site readable.
 */
package agent

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
	"golang.org/x/term"
)

// init normalizes go-runewidth so emoji-bearing content reports its
// real terminal-cell width. With the library defaults
// (EastAsianWidth=false, StrictEmojiNeutral=true) emojis such as
// "🏟️", "⚫", "🔴" are measured as 1 cell while almost every modern
// terminal renders them as 2 — and the drift compounded into right
// borders being pushed off the screen. Pinning StrictEmojiNeutral
// to false makes the measurement match the rendering across iTerm2,
// Terminal.app, VSCode, Windows Terminal, and Alacritty.
func init() {
	runewidth.DefaultCondition.StrictEmojiNeutral = false
	runewidth.DefaultCondition.EastAsianWidth = false
}

// TerminalWidth reports the live terminal width in columns, falling
// back to a safe default when stdout is not a TTY (CI, tests, piped
// runs). The 100-column fallback is wider than the legacy 87-col
// constant so piped runs still produce a readable box; callers that
// need a tighter cap clamp the result themselves.
func TerminalWidth() int {
	w, _, err := term.GetSize(int(os.Stdout.Fd())) //#nosec G115 -- bounded by domain
	if err != nil || w <= 0 {
		return 100
	}
	return w
}

// EnvelopeWidth returns the width to use for a response envelope on
// the current terminal. It reserves 2 columns of right-edge margin so
// iTerm/VSCode native scrollbars never clip the border, and clamps to
// a minimum of 40 cols so the box never collapses on tiny terminals.
// No upper cap is applied — full-screen terminals should use their
// full width (this was a direct user preference).
func EnvelopeWidth() int {
	w := TerminalWidth() - 2
	if w < 40 {
		return 40
	}
	return w
}

// ResponseEnvelopeOptions configures a unified bordered envelope. All
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

	// FooterLeft and FooterRight mirror the header on the bottom
	// border. Most callers leave them empty (the body's terminal
	// punctuation closes the thought) and the bottom is a plain
	// ╰────╯ line. Provided for future telemetry / status surfaces.
	FooterLeft  string
	FooterRight string

	// Body is the message content to render inside the box. Typically
	// glamour-rendered markdown (ANSI escapes preserved); the envelope
	// wraps it to the resolved inner width.
	Body string

	// Color is the package-local ANSI color constant used for borders
	// (e.g. ColorGray, ColorPurple). Maps to lipgloss internally.
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

// RenderResponseEnvelope paints the assistant's reply in a bordered
// box. Mechanics:
//
//  1. Resolve the cap (caller-pinned Width, otherwise live terminal).
//  2. Wrap body to inner = cap − borders (2) − padding (4).
//  3. Measure the widest wrapped body line.
//  4. Grow the card so it also fits the header & footer labels with
//     at least 2 dashes of breathing room on each side. Without this
//     step a short body + long header (model name + metrics) painted
//     a top border wider than the body+bottom and the box read as
//     broken — exactly the regression the user reported on 2026-05-20.
//  5. Pad every wrapped line to the chosen inner width so lipgloss
//     produces a rectangular block (no Width() needed, no surprises
//     from lipgloss's "minimum width" semantics).
//  6. Render body with lipgloss left + right borders only, then paint
//     bilateral top and bottom borders at the measured card width.
//  7. Optionally typewriter the body.
//
// The whole point of routing every border through lipgloss.Width is
// that emoji width disagreements stop mattering: every border row
// agrees with every other border row, even when they disagree with
// the terminal. Thanks to the init() normalization, they now usually
// agree with the terminal too.
func (r *UIRenderer) RenderResponseEnvelope(opts ResponseEnvelopeOptions) {
	cap := opts.Width
	if cap <= 0 {
		cap = EnvelopeWidth()
	}
	if cap < 24 {
		cap = 24
	}

	// innerOverhead: 2 borders + 4 horizontal padding (Padding(0,2)).
	const innerOverhead = 6
	maxInner := cap - innerOverhead
	if maxInner < 16 {
		maxInner = 16
	}

	body := strings.Trim(opts.Body, "\n\r")
	if body == "" {
		body = " " // lipgloss collapses fully-empty content; keep the box drawable
	}
	wrapped := wrapText(body, maxInner)

	bodyMax := 0
	for _, ln := range wrapped {
		if w := lipgloss.Width(ln); w > bodyMax {
			bodyMax = w
		}
	}

	// minLabelFill: dashes of breathing room around the header/footer
	// labels. Without this, a header that just barely fits would render
	// flush against both corners ("╭─Label─╮"), which reads as cramped.
	const minLabelFill = 2

	leftW := lipgloss.Width(opts.HeaderLeft)
	rightW := lipgloss.Width(opts.HeaderRight)
	hdrFloor := 0
	if leftW > 0 || rightW > 0 {
		hdrFloor = leftW + rightW + minLabelFill
	}

	flw := lipgloss.Width(opts.FooterLeft)
	frw := lipgloss.Width(opts.FooterRight)
	ftrFloor := 0
	if flw > 0 || frw > 0 {
		ftrFloor = flw + frw + minLabelFill
	}

	// chosenInner = max(bodyMax, header floor, footer floor), capped at
	// maxInner. This is the contract that fixes the "top wider than
	// body" bug: when header labels need more room than the body, the
	// card grows to fit them; the body just pads out to match.
	chosenInner := bodyMax
	if chosenInner < hdrFloor {
		chosenInner = hdrFloor
	}
	if chosenInner < ftrFloor {
		chosenInner = ftrFloor
	}
	if chosenInner > maxInner {
		chosenInner = maxInner
	}
	if chosenInner < 16 {
		chosenInner = 16
	}

	// Pad every wrapped line to chosenInner so lipgloss renders a
	// rectangular block. Skipping this and relying on lipgloss .Width()
	// is tempting but lipgloss's width semantics make the math fragile
	// once Padding is in the mix; padding here keeps it explicit.
	padded := make([]string, 0, len(wrapped))
	for _, ln := range wrapped {
		gap := chosenInner - lipgloss.Width(ln)
		if gap < 0 {
			gap = 0
		}
		padded = append(padded, ln+strings.Repeat(" ", gap))
	}

	bodyStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderTop(false).
		BorderBottom(false).
		BorderForeground(ansiColorToLip(opts.Color)).
		Padding(0, 2)

	bodyRendered := bodyStyle.Render(strings.Join(padded, "\n"))
	bodyRendered = trimBlankBoxBodyRows(bodyRendered)

	// Anchoring both borders to lipgloss.Width(bodyRendered) keeps
	// every row in agreement on visible width, even under emoji-width
	// drift.
	cardWidth := lipgloss.Width(bodyRendered)

	topLine := buildBilateralBorder('╭', '╮', opts.HeaderLeft, opts.HeaderRight, cardWidth, opts.Color, r)
	bottomLine := buildBilateralBorder('╰', '╯', opts.FooterLeft, opts.FooterRight, cardWidth, opts.Color, r)

	delay := opts.TypewriterDelay
	if delay == 0 {
		delay = 2 * time.Millisecond
	}

	fmt.Println()
	fmt.Println(topLine)
	if opts.Typewriter {
		typewriterPrint(bodyRendered, delay)
		fmt.Println()
	} else {
		fmt.Println(bodyRendered)
	}
	fmt.Println(bottomLine)
}

// buildBilateralBorder constructs a horizontal border with optional
// left and right labels embedded between the corner glyphs:
//
//	<lc>─ HeaderLeft ──────── HeaderRight ─<rc>
//
// Layout rules (in visible columns):
//   - <lc> + '─' + leftLabel  if leftLabel != ""   (else <lc> + '─')
//   - fill of '─' to absorb remaining width
//   - rightLabel + '─' + <rc> if rightLabel != ""  (else '─' + <rc>)
//
// targetWidth is the EXACT visible width we must produce, measured by
// lipgloss.Width on the matching body. A degenerate case where the
// labels alone exceed targetWidth falls back to a minimal border (the
// labels survive; the fill goes to zero).
func buildBilateralBorder(lc, rc rune, leftLabel, rightLabel string, targetWidth int, color string, r *UIRenderer) string {
	const cornerCols = 1   // lc / rc themselves
	const dashCornerPad = 1 // the "─" that hugs each corner

	leftBlock := string(lc) + "─"
	rightBlock := "─" + string(rc)
	reserved := cornerCols*2 + dashCornerPad*2 // 4 cols of "<lc>─...─<rc>"

	leftVis := lipgloss.Width(leftLabel)
	rightVis := lipgloss.Width(rightLabel)

	fill := targetWidth - reserved - leftVis - rightVis
	if fill < 0 {
		// Labels overflow the target — emit minimal border with the
		// labels intact so callers can still read them. This is a
		// safety net; in practice the envelope sizes the body to
		// accommodate normal labels.
		return r.Colorize(leftBlock+leftLabel+rightLabel+rightBlock, color+ColorBold)
	}

	dashes := strings.Repeat("─", fill)

	var sb strings.Builder
	sb.WriteString(leftBlock)
	if leftVis > 0 {
		sb.WriteString(leftLabel)
	}
	sb.WriteString(dashes)
	if rightVis > 0 {
		sb.WriteString(rightLabel)
	}
	sb.WriteString(rightBlock)
	return r.Colorize(sb.String(), color+ColorBold)
}
