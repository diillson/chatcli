/*
 * ChatCLI - Adaptive typewriter pacing
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * The reply card uses a typewriter effect to make the model's answer
 * feel alive. A naive fixed per-rune delay (2ms) works fine for a
 * handful of sentences but compounds badly on long replies — a 3 000
 * rune reply at 2ms is 6 seconds of cursor-sweeping-across-borders,
 * which feels broken instead of alive.
 *
 * PaceText adapts the cadence:
 *
 *   - Short replies keep the caller-requested delay (the effect
 *     reads as animation).
 *   - Long replies have their delay scaled DOWN so the whole
 *     animation completes within a target budget (default 800ms).
 *   - Replies above a hard threshold (8 000 visible runes) skip the
 *     animation entirely — painting a giant code block one rune at a
 *     time is never the right call.
 *
 * Three environment variables let advanced users tune the behavior
 * without rebuilding:
 *
 *   CHATCLI_NO_TYPEWRITER=1          skip animation entirely
 *   CHATCLI_TYPEWRITER_BUDGET_MS=N   override the total budget in ms
 *                                    (0 disables the budget; caller
 *                                    delay is used verbatim)
 *   CHATCLI_TYPEWRITER_DELAY_MS=N    override the per-rune base delay
 *
 * The pacing is centralized here so every surface that types out
 * model output (chat envelope, agent RESPOSTA card, coder summary,
 * /command relay) converges on the same UX.
 */
package agent

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	// defaultBudget caps the total animation time at ~800ms. Picked by
	// feel: under 1s the eye still reads it as animation, beyond that
	// the user starts perceiving it as latency. Tuneable via env var.
	defaultBudget = 800 * time.Millisecond

	// defaultDelay matches what the chat envelope used historically so
	// existing tests / muscle memory are preserved for short bodies.
	defaultDelay = 2 * time.Millisecond

	// minDelay floors the per-rune sleep at 200μs so the budget path
	// doesn't degenerate into a fixed-overhead syscall storm. Sleeps
	// below ~100μs are mostly noise on macOS/Linux scheduling anyway.
	minDelay = 200 * time.Microsecond

	// hardSkipChars: bodies with more visible runes than this skip
	// the animation outright. 8k chars × even 200μs is already 1.6s
	// of cursor sweep, and most replies that large are dumps of code
	// where the animation adds zero value.
	hardSkipChars = 8000
)

// PaceText prints text with an adaptive typewriter cadence. The
// requested delay is used as a ceiling — the actual per-rune sleep
// may be scaled down to fit the configured budget on long bodies, or
// zeroed out entirely when the body exceeds hardSkipChars or the
// user disabled the effect via CHATCLI_NO_TYPEWRITER.
//
// ANSI escape sequences embedded in text are emitted instantly (no
// sleep between escape bytes) so color transitions never pause the
// eye — the same behavior typewriterPrint had before this refactor.
func PaceText(text string, requested time.Duration) {
	if typewriterDisabled() {
		fmt.Print(text)
		return
	}

	budget := resolveBudget()
	requested = resolveDelay(requested)

	printable := countPrintableRunes(text)
	if printable >= hardSkipChars {
		fmt.Print(text)
		return
	}

	effective := requested
	if budget > 0 && printable > 0 {
		// Scale down so total animation ≤ budget. Never scale UP — the
		// requested delay is a ceiling; if a caller asked for slow,
		// they get slow (for short bodies).
		if perChar := budget / time.Duration(printable); perChar < effective {
			effective = perChar
		}
	}
	if effective < minDelay {
		effective = minDelay
	}

	typewriterPrint(text, effective)
}

// resolveBudget reads CHATCLI_TYPEWRITER_BUDGET_MS (a non-negative
// integer in milliseconds) and returns the configured budget. Unset
// or invalid → defaultBudget. Explicit 0 disables the budget, letting
// the caller's requested delay run verbatim.
func resolveBudget() time.Duration {
	if s := strings.TrimSpace(os.Getenv("CHATCLI_TYPEWRITER_BUDGET_MS")); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n >= 0 {
			return time.Duration(n) * time.Millisecond
		}
	}
	return defaultBudget
}

// resolveDelay applies the CHATCLI_TYPEWRITER_DELAY_MS override on
// top of the caller's requested delay. The override wins when set —
// power users typing CHATCLI_TYPEWRITER_DELAY_MS=0 should see instant
// paint regardless of what the surface asked for.
func resolveDelay(requested time.Duration) time.Duration {
	if s := strings.TrimSpace(os.Getenv("CHATCLI_TYPEWRITER_DELAY_MS")); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n >= 0 {
			return time.Duration(n) * time.Millisecond
		}
	}
	if requested <= 0 {
		return defaultDelay
	}
	return requested
}

// typewriterDisabled returns true when the user opted out of the
// animation via CHATCLI_NO_TYPEWRITER. Accepts the common truthy
// values so users don't have to guess at the exact spelling.
func typewriterDisabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("CHATCLI_NO_TYPEWRITER")))
	switch v {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// countPrintableRunes counts runes outside ANSI CSI sequences. The
// printable count drives the budget math (and the hardSkip decision)
// because that's what the user actually sees the cursor sweep across.
// Mirrors the inEsc bookkeeping inside typewriterPrint so the two
// stay in agreement on what "printable" means.
func countPrintableRunes(s string) int {
	n := 0
	inEsc := false
	for _, ch := range s {
		if ch == '\033' {
			inEsc = true
			continue
		}
		if inEsc {
			if ch == 'm' {
				inEsc = false
			}
			continue
		}
		n++
	}
	return n
}
