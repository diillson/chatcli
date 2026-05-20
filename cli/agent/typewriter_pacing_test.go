/*
 * ChatCLI - tests for adaptive typewriter pacing
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * These tests defend the contracts that turned the typewriter from
 * "feels alive" into "completes in time" for long replies:
 *
 *   1. Short bodies keep the requested per-rune delay (the effect is
 *      preserved at small sizes — that's what the user signed up for).
 *   2. Long bodies have their delay scaled down so the total
 *      animation fits the configured budget.
 *   3. Bodies above hardSkipChars skip the animation entirely.
 *   4. ANSI escape sequences don't count as printable runes.
 *   5. The CHATCLI_NO_TYPEWRITER / BUDGET_MS / DELAY_MS env knobs
 *      take effect without restart.
 *
 * Timing-sensitive checks tolerate generous slack (>=2x the target)
 * because Go's time.Sleep granularity on Linux/macOS is around 1ms
 * and CI runners can be noisy.
 */

package agent

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// drainStdout swaps stdout for the duration of fn, returning whatever
// fn wrote. Used so the timing tests don't pollute the test runner
// console with cursor-paced output.
func drainStdout(t *testing.T, fn func()) {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, r)
		close(done)
	}()
	fn()
	_ = w.Close()
	os.Stdout = old
	<-done
}

// TestCountPrintableRunes_IgnoresANSI locks the helper that drives
// the budget math: ANSI CSI sequences (\x1b[...m) must not count
// toward the printable runes. If they did, an ANSI-heavy short reply
// (typical of glamour-rendered markdown) would be misclassified as
// "long" and the animation would skip.
func TestCountPrintableRunes_IgnoresANSI(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want int
	}{
		{"plain ASCII", "hello", 5},
		{"with reset", "\x1b[0mhello\x1b[0m", 5},
		{"bold + color", "\x1b[1;31mhi\x1b[0m", 2},
		{"empty", "", 0},
		{"ANSI only", "\x1b[35m\x1b[0m", 0},
		{"emoji counts as runes", "🔥🔥🔥", 3},
	}
	for _, tc := range cases {
		got := countPrintableRunes(tc.in)
		assert.Equalf(t, tc.want, got, "case %q", tc.name)
	}
}

// TestPaceText_ShortBodyKeepsRequestedCadence proves that a 20-rune
// reply at 2ms per rune budgets to ~40ms — well under the 800ms
// default budget — so the requested cadence flows through unchanged.
// We measure elapsed time and assert it sits inside a reasonable
// window around the expected.
func TestPaceText_ShortBodyKeepsRequestedCadence(t *testing.T) {
	t.Setenv("CHATCLI_NO_TYPEWRITER", "")
	t.Setenv("CHATCLI_TYPEWRITER_BUDGET_MS", "")
	t.Setenv("CHATCLI_TYPEWRITER_DELAY_MS", "")

	body := strings.Repeat("a", 20)
	requested := 5 * time.Millisecond

	start := time.Now()
	drainStdout(t, func() {
		PaceText(body, requested)
	})
	elapsed := time.Since(start)

	// 20 runes × 5ms = 100ms expected. Tolerate scheduler noise.
	assert.GreaterOrEqualf(t, elapsed, 80*time.Millisecond,
		"short body must honor the requested per-rune cadence (got %v)", elapsed)
	assert.LessOrEqualf(t, elapsed, 500*time.Millisecond,
		"short body must not drift far past expected cadence (got %v)", elapsed)
}

// TestPaceText_LongBodyFitsBudget proves that a 2 000-rune reply
// completes within the 800ms budget even when the caller asked for
// 2ms per rune (which naively would be 4 seconds).
func TestPaceText_LongBodyFitsBudget(t *testing.T) {
	t.Setenv("CHATCLI_NO_TYPEWRITER", "")
	t.Setenv("CHATCLI_TYPEWRITER_BUDGET_MS", "")
	t.Setenv("CHATCLI_TYPEWRITER_DELAY_MS", "")

	body := strings.Repeat("a", 2000)

	start := time.Now()
	drainStdout(t, func() {
		PaceText(body, 2*time.Millisecond)
	})
	elapsed := time.Since(start)

	// Must finish under twice the budget — accounting for scheduler
	// noise; the scaled per-rune delay should bring it well under
	// 1600ms even on a slow CI runner.
	assert.LessOrEqualf(t, elapsed, 1600*time.Millisecond,
		"long body must fit within ~2× budget (got %v)", elapsed)
}

// TestPaceText_VeryLongBodySkipsAnimation guards the hardSkipChars
// shortcut: a body with > 8 000 visible runes must paint near-
// instantly with no per-rune sleeping.
func TestPaceText_VeryLongBodySkipsAnimation(t *testing.T) {
	t.Setenv("CHATCLI_NO_TYPEWRITER", "")
	t.Setenv("CHATCLI_TYPEWRITER_BUDGET_MS", "")
	t.Setenv("CHATCLI_TYPEWRITER_DELAY_MS", "")

	body := strings.Repeat("a", hardSkipChars+1)

	start := time.Now()
	drainStdout(t, func() {
		PaceText(body, 2*time.Millisecond)
	})
	elapsed := time.Since(start)

	// Allow up to 100ms for the raw fmt.Print on 8 KB. No per-rune
	// sleeps means this should be effectively instantaneous.
	assert.LessOrEqualf(t, elapsed, 100*time.Millisecond,
		"bodies above hardSkipChars must skip the animation (got %v)", elapsed)
}

// TestPaceText_NoTypewriterEnvDisables guards the user-facing kill
// switch: setting CHATCLI_NO_TYPEWRITER=1 must paint instantly.
func TestPaceText_NoTypewriterEnvDisables(t *testing.T) {
	t.Setenv("CHATCLI_NO_TYPEWRITER", "1")

	body := strings.Repeat("a", 500)

	start := time.Now()
	drainStdout(t, func() {
		PaceText(body, 5*time.Millisecond)
	})
	elapsed := time.Since(start)

	assert.LessOrEqualf(t, elapsed, 50*time.Millisecond,
		"CHATCLI_NO_TYPEWRITER must skip all sleeping (got %v)", elapsed)
}

// TestPaceText_BudgetEnvOverride lets the user shrink or grow the
// total budget. A budget of 100ms on a 200-rune body should bring
// total time under ~300ms (budget + scheduler slack), versus the
// default 800ms which would naturally fit too.
func TestPaceText_BudgetEnvOverride(t *testing.T) {
	t.Setenv("CHATCLI_NO_TYPEWRITER", "")
	t.Setenv("CHATCLI_TYPEWRITER_BUDGET_MS", "100")
	t.Setenv("CHATCLI_TYPEWRITER_DELAY_MS", "")

	body := strings.Repeat("a", 200)

	start := time.Now()
	drainStdout(t, func() {
		PaceText(body, 5*time.Millisecond)
	})
	elapsed := time.Since(start)

	// 200 runes × 5ms = 1s requested; budget 100ms scales it down.
	// Allow up to 4× the budget for scheduler noise on slow CI.
	assert.LessOrEqualf(t, elapsed, 400*time.Millisecond,
		"CHATCLI_TYPEWRITER_BUDGET_MS=100 must scale down the delay (got %v)", elapsed)
}

// TestPaceText_DelayEnvOverride lets the user force a per-rune delay
// regardless of what the caller passed.
func TestPaceText_DelayEnvOverride(t *testing.T) {
	t.Setenv("CHATCLI_NO_TYPEWRITER", "")
	t.Setenv("CHATCLI_TYPEWRITER_BUDGET_MS", "0")
	t.Setenv("CHATCLI_TYPEWRITER_DELAY_MS", "0")

	body := strings.Repeat("a", 200)

	start := time.Now()
	drainStdout(t, func() {
		PaceText(body, 50*time.Millisecond) // caller asks slow
	})
	elapsed := time.Since(start)

	// With CHATCLI_TYPEWRITER_DELAY_MS=0 (and budget disabled) the
	// effective delay falls to minDelay (200μs). 200 × 200μs = 40ms.
	assert.LessOrEqualf(t, elapsed, 200*time.Millisecond,
		"CHATCLI_TYPEWRITER_DELAY_MS=0 must override the caller delay (got %v)", elapsed)
}

// TestPaceText_EmitsAllBytes safety check: regardless of pacing,
// every byte of the input must reach stdout. A regression here would
// surface as truncated replies.
func TestPaceText_EmitsAllBytes(t *testing.T) {
	t.Setenv("CHATCLI_NO_TYPEWRITER", "1") // skip sleeping for speed

	body := "Hello, \x1b[31mworld\x1b[0m! 🔥 — this is a paced reply."

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	done := make(chan []byte)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.Bytes()
	}()
	PaceText(body, 0)
	_ = w.Close()
	os.Stdout = old
	got := <-done

	assert.Equal(t, body, string(got), "every byte must round-trip through PaceText")
}
