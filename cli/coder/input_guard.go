package coder

import (
	"context"
	"time"

	"go.uber.org/zap"
)

// DefaultIntentDebounce is the post-mount window during which any input
// arriving on the channel is treated as accidental typeahead and discarded.
// 250ms is long enough to catch keystrokes the user emitted *while* the
// security prompt was rendering, but short enough to be invisible during
// deliberate interaction.
const DefaultIntentDebounce = 250 * time.Millisecond

// InputGuard hardens user-facing confirmation prompts against accidental
// answers caused by typeahead. The user's threat model here is not malicious:
// it's the user typing during an LLM stream and unintentionally pre-answering
// the next security prompt with whatever happened to be in the buffer.
//
// The guard works in three layers, each defeating a different race:
//
//  1. FlushTTYInput  — clears the kernel-side TTY input buffer (chars typed
//     before our reader goroutine consumed them).
//  2. DrainStdinChannel — drains the buffered channel between the reader
//     goroutine and the prompt handler (chars already consumed by us but not
//     yet read by the prompt).
//  3. IntentDebounce — after the UI is on screen, discard any input that
//     arrives within a short window (catches keystrokes that were *already in
//     flight* when steps 1+2 ran, and gives the user time to react to the
//     prompt before their typing counts).
//
// All three are best-effort: failures are logged at DEBUG and the prompt
// continues. The worst case if every layer fails is the legacy behavior
// (which is what we are improving), not a security regression.
type InputGuard struct {
	logger         *zap.Logger
	debounceWindow time.Duration
	flushTTYFn     func() error
}

// NewInputGuard constructs a guard with sensible defaults. A nil logger is
// replaced with a no-op logger so callers in non-logged paths don't crash.
func NewInputGuard(logger *zap.Logger) *InputGuard {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &InputGuard{
		logger:         logger,
		debounceWindow: DefaultIntentDebounce,
		flushTTYFn:     flushTTYInput,
	}
}

// WithDebounceWindow overrides the post-mount debounce duration. A value of
// zero or negative disables debouncing entirely (useful for unit tests).
func (g *InputGuard) WithDebounceWindow(d time.Duration) *InputGuard {
	g.debounceWindow = d
	return g
}

// DrainStdinChannel non-blockingly empties the channel and returns the
// discarded lines. The caller is responsible for any logging beyond the
// DEBUG-level summary emitted here.
func (g *InputGuard) DrainStdinChannel(ch <-chan string) []string {
	if ch == nil {
		return nil
	}
	var discarded []string
	for {
		select {
		case line, ok := <-ch:
			if !ok {
				return discarded
			}
			discarded = append(discarded, line)
		default:
			if len(discarded) > 0 {
				g.logger.Debug("input guard: drained pending stdin lines before security prompt",
					zap.Int("count", len(discarded)),
					zap.String("first_preview", preview(discarded[0])))
			}
			return discarded
		}
	}
}

// FlushTTYInput discards any unread input bytes still buffered by the kernel
// for the controlling terminal. On platforms where the operation is not
// available (no TTY, sandboxed CI, Windows without a console), it returns nil
// — the higher layers (channel drain + debounce) still apply.
func (g *InputGuard) FlushTTYInput() error {
	if g.flushTTYFn == nil {
		return nil
	}
	return g.flushTTYFn()
}

// IntentDebounce reads-and-discards any input arriving on ch during the
// configured window. Returns the count of discarded lines. The context is
// honored: cancellation aborts the wait without blocking.
func (g *InputGuard) IntentDebounce(ctx context.Context, ch <-chan string) int {
	if g.debounceWindow <= 0 || ch == nil {
		return 0
	}
	timer := time.NewTimer(g.debounceWindow)
	defer timer.Stop()

	discarded := 0
	for {
		select {
		case <-ctx.Done():
			return discarded
		case <-timer.C:
			if discarded > 0 {
				g.logger.Debug("input guard: debounced spurious input during intent window",
					zap.Int("count", discarded),
					zap.Duration("window", g.debounceWindow))
			}
			return discarded
		case line, ok := <-ch:
			if !ok {
				return discarded
			}
			_ = line
			discarded++
		}
	}
}

// Guard runs the full pre-prompt sequence: flush TTY → drain channel.
// Callers should invoke this BEFORE rendering the UI, then call
// IntentDebounce AFTER the UI is rendered.
//
// Returns true if any layer discarded user input — callers may want to
// surface this in the UI ("ignored prefilled input").
func (g *InputGuard) Guard(ch <-chan string) bool {
	anyDiscarded := false
	if err := g.FlushTTYInput(); err != nil {
		g.logger.Debug("input guard: FlushTTYInput failed (non-fatal)", zap.Error(err))
	}
	if drained := g.DrainStdinChannel(ch); len(drained) > 0 {
		anyDiscarded = true
	}
	return anyDiscarded
}

// preview returns a short, escape-stripped excerpt of an input line for
// safe logging. We never want to dump full user input to the log.
func preview(s string) string {
	const maxLen = 24
	out := make([]rune, 0, maxLen)
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			continue
		}
		out = append(out, r)
		if len(out) >= maxLen {
			out = append(out, '…')
			break
		}
	}
	return string(out)
}
