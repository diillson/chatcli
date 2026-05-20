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

	// tickInterval is the wall-clock cadence used when the renderer
	// switches into chunked mode for long bodies. 10ms = 100 ticks per
	// second is fast enough to still read as animation but slow enough
	// that scheduler noise (1-2ms on Linux/macOS) becomes negligible.
	// This is why long-body timing tests stay deterministic — every
	// sleep call is well above the OS scheduler's granularity.
	tickInterval = 10 * time.Millisecond

	// hardSkipChars: bodies with more visible runes than this skip
	// the animation outright. 8k chars worth of animation feels like
	// latency, and most replies that large are dumps of code where
	// the animation adds zero value.
	hardSkipChars = 8000
)

// PaceText prints text with an adaptive typewriter cadence. Short
// bodies use rune-by-rune animation at the requested delay (the
// effect reads as animation); long bodies switch to a chunked mode
// where multiple runes paint per ~10ms tick so the total animation
// completes within the configured budget; very long bodies skip the
// animation entirely.
//
// Why two modes instead of one: a naive "scale down per-rune delay"
// approach degenerates below the OS scheduler's granularity (~1-2ms
// on Linux/macOS). With 2 000 runes and an 800ms budget the per-rune
// math says 400μs/rune but the actual wall-clock can balloon to 4s on
// CI under noise. Chunking sidesteps that: each sleep is a full 10ms,
// well above scheduler granularity, and we just emit more runes per
// tick to hit the same total time. The result is deterministically
// bounded by the budget regardless of how the host schedules sleeps.
//
// ANSI escape sequences embedded in text are emitted as part of the
// chunk they land in — they don't trigger sleeps or count toward the
// printable budget so color transitions never pause the eye.
func PaceText(text string, requested time.Duration) {
	if typewriterDisabled() {
		fmt.Print(text)
		return
	}

	requested = resolveDelay(requested)
	budget := resolveBudget()

	printable := countPrintableRunes(text)
	if printable == 0 || requested <= 0 {
		fmt.Print(text)
		return
	}
	if printable >= hardSkipChars {
		fmt.Print(text)
		return
	}

	// Path A — short body: requested cadence fits the budget, so we
	// keep the per-rune animation. Most chat replies live here.
	requestedTotal := time.Duration(printable) * requested
	if budget <= 0 || requestedTotal <= budget {
		typewriterPrint(text, requested)
		return
	}

	// Path B — long body: switch to chunked mode. Spread the printable
	// runes evenly across the budget at tickInterval cadence.
	totalTicks := int(budget / tickInterval)
	if totalTicks < 1 {
		totalTicks = 1
	}
	runesPerTick := (printable + totalTicks - 1) / totalTicks
	if runesPerTick < 1 {
		runesPerTick = 1
	}
	chunkedTypewriterPrint(text, runesPerTick, tickInterval)
}

// chunkedTypewriterPrint walks text writing runesPerTick visible runes
// per pass and sleeping interval between passes. ANSI escape sequences
// are emitted with the printable rune that triggered the chunk to keep
// color transitions atomic — splitting a CSI sequence across a sleep
// would render as a visible flicker.
//
// Compared to typewriterPrint (one sleep per rune), this caps the
// total number of sleeps at budget/interval, making the wall-clock
// time predictable even on slow CI runners. The trade-off is that
// the eye sees small bursts of text instead of single characters, but
// at ~10ms intervals it still reads as fluid animation.
func chunkedTypewriterPrint(text string, runesPerTick int, interval time.Duration) {
	var buf strings.Builder
	inEsc := false
	chunkRunes := 0

	flush := func() {
		if buf.Len() == 0 {
			return
		}
		fmt.Print(buf.String())
		_ = os.Stdout.Sync()
		buf.Reset()
		chunkRunes = 0
	}

	for _, ch := range text {
		buf.WriteRune(ch)
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
		chunkRunes++
		if chunkRunes >= runesPerTick {
			flush()
			time.Sleep(interval)
		}
	}
	flush()
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
