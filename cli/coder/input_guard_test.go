package coder

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

// TestInputGuard_DrainStdinChannel_EmptiesPendingLines verifies that lines
// already buffered in the channel before the prompt mounts are discarded.
// This is the classic typeahead scenario: user types "y\n" while the LLM
// is streaming, the line reaches stdinLines, then the security prompt
// shows up and (without the guard) would consume that "y" as the answer.
func TestInputGuard_DrainStdinChannel_EmptiesPendingLines(t *testing.T) {
	ch := make(chan string, 10)
	ch <- "y"
	ch <- "stray paste"
	ch <- "another"

	g := NewInputGuard(zap.NewNop())
	discarded := g.DrainStdinChannel(ch)

	assert.Equal(t, []string{"y", "stray paste", "another"}, discarded)
	assert.Equal(t, 0, len(ch), "channel must be empty after drain")
}

// TestInputGuard_DrainStdinChannel_NilSafe ensures the helper does not panic
// when called with a nil channel (the agent mode codepath when stdin reader
// has not been started).
func TestInputGuard_DrainStdinChannel_NilSafe(t *testing.T) {
	g := NewInputGuard(zap.NewNop())
	assert.Nil(t, g.DrainStdinChannel(nil))
}

// TestInputGuard_DrainStdinChannel_NonBlocking confirms the drain returns
// immediately once the channel is empty, even if no value is ever sent.
// Without the `default` arm in the select, this would deadlock.
func TestInputGuard_DrainStdinChannel_NonBlocking(t *testing.T) {
	ch := make(chan string)
	g := NewInputGuard(zap.NewNop())

	done := make(chan struct{})
	go func() {
		g.DrainStdinChannel(ch)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("DrainStdinChannel blocked on empty channel")
	}
}

// TestInputGuard_IntentDebounce_DiscardsInWindow simulates a user who types
// just after the security UI is rendered. The guard must discard those
// keystrokes (they were already in flight when the UI appeared) and only
// allow input typed AFTER the debounce window to flow through.
func TestInputGuard_IntentDebounce_DiscardsInWindow(t *testing.T) {
	ch := make(chan string, 4)
	g := NewInputGuard(zap.NewNop()).WithDebounceWindow(80 * time.Millisecond)

	// Pre-feed two "spurious" lines that would arrive during the window.
	ch <- "y"
	ch <- "yes"

	discarded := g.IntentDebounce(context.Background(), ch)
	assert.Equal(t, 2, discarded)
	assert.Equal(t, 0, len(ch))
}

// TestInputGuard_IntentDebounce_RespectsContext verifies that ctx
// cancellation aborts the wait without consuming a partial window. This
// matters during Ctrl+C while the prompt is up.
func TestInputGuard_IntentDebounce_RespectsContext(t *testing.T) {
	ch := make(chan string)
	g := NewInputGuard(zap.NewNop()).WithDebounceWindow(5 * time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	g.IntentDebounce(ctx, ch)
	assert.Less(t, time.Since(start), 200*time.Millisecond,
		"ctx cancellation must abort debounce immediately")
}

// TestInputGuard_IntentDebounce_ZeroWindowIsNoOp lets callers disable the
// post-mount window (useful when calling code wants to control flow).
func TestInputGuard_IntentDebounce_ZeroWindowIsNoOp(t *testing.T) {
	ch := make(chan string, 1)
	ch <- "leftover"
	g := NewInputGuard(zap.NewNop()).WithDebounceWindow(0)

	got := g.IntentDebounce(context.Background(), ch)
	assert.Equal(t, 0, got)
	assert.Equal(t, 1, len(ch), "zero window must not consume input")
}

// TestInputGuard_Guard_HappyPath chains the full sequence: flush TTY +
// drain channel. Returns true when something was discarded.
func TestInputGuard_Guard_HappyPath(t *testing.T) {
	ch := make(chan string, 2)
	ch <- "preloaded"
	g := NewInputGuard(zap.NewNop())

	assert.True(t, g.Guard(ch))
	assert.Equal(t, 0, len(ch))
}

// TestInputGuard_Guard_ReportsCleanWhenEmpty ensures the caller can rely on
// the returned bool to know whether typeahead was suppressed (useful for
// surfacing in the UI: "ignored 2 pre-typed lines").
func TestInputGuard_Guard_ReportsCleanWhenEmpty(t *testing.T) {
	ch := make(chan string, 2)
	g := NewInputGuard(zap.NewNop())

	assert.False(t, g.Guard(ch))
}

// TestInputGuard_DrainLogsDebugCountWithoutLeakingContent guarantees that
// when input is drained, the log entry includes a count and a *redacted*
// preview but never the full user text. We log a preview of the first
// discarded line to help debug "why was my input ignored?" without dumping
// potentially sensitive paste.
func TestInputGuard_DrainLogsDebugCountWithoutLeakingContent(t *testing.T) {
	core, recorded := observer.New(zap.DebugLevel)
	g := NewInputGuard(zap.New(core))

	ch := make(chan string, 3)
	ch <- "echo my-secret-token-very-long-value-1234567890abcdef"
	ch <- "second"

	g.DrainStdinChannel(ch)

	entries := recorded.FilterMessage("input guard: drained pending stdin lines before security prompt").All()
	require.Len(t, entries, 1, "exactly one drain log entry expected")
	fields := entries[0].ContextMap()
	assert.Equal(t, int64(2), fields["count"])
	preview, _ := fields["first_preview"].(string)
	assert.NotContains(t, preview, "1234567890abcdef",
		"log preview must not include the tail of long lines (it should be truncated with an ellipsis)")
}

// TestInputGuard_DebounceLogsOnlyWhenSomethingDiscarded keeps the noise
// floor low: in normal interactive use, the debounce window is hit but no
// stray input arrives, and we don't want a log line every prompt.
func TestInputGuard_DebounceLogsOnlyWhenSomethingDiscarded(t *testing.T) {
	core, recorded := observer.New(zap.DebugLevel)
	g := NewInputGuard(zap.New(core)).WithDebounceWindow(40 * time.Millisecond)

	ch := make(chan string)
	g.IntentDebounce(context.Background(), ch)

	entries := recorded.FilterMessage("input guard: debounced spurious input during intent window").All()
	assert.Empty(t, entries, "no log when window expired with no input")
}

// TestInputGuard_ConcurrentDrainAndProduce documents the contract under a
// concurrent producer: any line written *before* drain starts is captured;
// lines written *after* drain returns are left in the channel. There is no
// guarantee that a write racing with drain is captured, which is fine
// — those will be caught by the post-mount debounce.
func TestInputGuard_ConcurrentDrainAndProduce(t *testing.T) {
	ch := make(chan string, 8)
	for i := 0; i < 4; i++ {
		ch <- "before"
	}

	var wg sync.WaitGroup
	g := NewInputGuard(zap.NewNop())

	wg.Add(1)
	var drained []string
	go func() {
		defer wg.Done()
		drained = g.DrainStdinChannel(ch)
	}()
	wg.Wait()

	assert.GreaterOrEqual(t, len(drained), 4)
}
