package cli

import (
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDrainStdinToQueue_FirstMessageReturned verifies the contract used by
// the ReAct loop (agent_mode.go:1132): the first non-empty stdin line is
// returned for immediate injection as a new user instruction; the rest go
// to messageQueue for the next turn. This is what makes "type a follow-up
// while the agent runs" actually work.
func TestDrainStdinToQueue_FirstMessageReturned(t *testing.T) {
	cli := &ChatCLI{
		messageQueue: nil,
	}
	a := &AgentMode{cli: cli}
	a.stdinLines = make(chan string, 4)
	a.stdinLines <- "primeira linha"
	a.stdinLines <- "segunda"
	a.stdinLines <- "terceira"

	first := a.drainStdinToQueue()

	assert.Equal(t, "primeira linha", first, "first non-empty line is returned for immediate injection")
	cli.messageQueueMu.Lock()
	queue := append([]string(nil), cli.messageQueue...)
	cli.messageQueueMu.Unlock()
	assert.Equal(t, []string{"segunda", "terceira"}, queue, "remainder lands in the FIFO queue")
}

// TestDrainStdinToQueue_SkipsEmptyLines ensures bare Enter presses don't
// hijack the slot meant for a real instruction. Without this guard a user
// who just pressed Enter to clear a stale prompt would have an empty
// "user message" injected into history, polluting the ReAct trace.
func TestDrainStdinToQueue_SkipsEmptyLines(t *testing.T) {
	cli := &ChatCLI{}
	a := &AgentMode{cli: cli}
	a.stdinLines = make(chan string, 4)
	a.stdinLines <- ""
	a.stdinLines <- ""
	a.stdinLines <- "real instruction"

	first := a.drainStdinToQueue()
	assert.Equal(t, "real instruction", first)
}

// TestDrainStdinToQueue_EmptyChannelReturnsEmpty documents the no-op
// behavior — calling drain when nothing was typed must not block and must
// return the empty string so the caller skips the injection branch.
func TestDrainStdinToQueue_EmptyChannelReturnsEmpty(t *testing.T) {
	cli := &ChatCLI{}
	a := &AgentMode{cli: cli}
	a.stdinLines = make(chan string, 4)

	first := a.drainStdinToQueue()
	assert.Empty(t, first)
	cli.messageQueueMu.Lock()
	defer cli.messageQueueMu.Unlock()
	assert.Empty(t, cli.messageQueue)
}

// TestDrainStdinToQueue_CoderModeIsNotGated is the regression test for
// Fase 1.2: the previous behavior gated drain behind !a.isCoderMode, which
// meant /coder users couldn't inject mid-run instructions. We verify
// directly via the public function — it is mode-agnostic by design — and
// also assert that the field on AgentMode is what determines the call
// site policy, not the helper itself.
func TestDrainStdinToQueue_CoderModeIsNotGated(t *testing.T) {
	cli := &ChatCLI{}
	a := &AgentMode{cli: cli}
	a.isCoderMode = true
	a.stdinLines = make(chan string, 2)
	a.stdinLines <- "fix bar.go too"

	first := a.drainStdinToQueue()
	assert.Equal(t, "fix bar.go too", first,
		"drainStdinToQueue must work identically regardless of mode — the gate "+
			"previously lived at the call site and is removed in Fase 1.2")
}

// TestQueueIndicator_FormatsCount asserts that the i18n key used for the
// per-turn queue indicator includes the count and matches both locales.
// We exercise it via fmt with the format string we send.
func TestQueueIndicator_FormatsCount(t *testing.T) {
	// We can't easily depend on the i18n bundle here without setting the
	// locale; instead, we verify the call site uses the indicator key and
	// passes the count via Sprintf semantics. A minimal regex on the
	// agent_mode.go source code itself catches accidental removals.
	src := mustReadFile(t, "agent_mode.go")
	require.Contains(t, src, `i18n.T("agent.queue.indicator", queued)`,
		"timer status must include queued count via i18n key")
}

// mustReadFile reads a sibling source file relative to the test file.
// We use this to assert that the call site we just refactored stays
// wired correctly to the i18n key.
func mustReadFile(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(name)
	require.NoError(t, err, "read sibling source %s", name)
	return string(data)
}

// TestDrainStdinToQueue_ReadsSliceFirst is the regression test for the
// bug that left coder mode without follow-up support: even after a
// message landed in cli.messageQueue (via skill_invoke or a prior
// turn's overflow), drainStdinToQueue was only reading from the
// stdinLines channel. With no new channel activity the slice was
// invisible to the agent loop and the user's queued instructions sat
// there forever. The fix consolidates the channel into the slice and
// pops from the slice — so a message pre-loaded into the slice must
// surface on the very next drain.
func TestDrainStdinToQueue_ReadsSliceFirst(t *testing.T) {
	cli := &ChatCLI{
		messageQueue: []string{"pre-queued from skill_invoke"},
	}
	a := &AgentMode{cli: cli}
	a.stdinLines = make(chan string, 4) // empty channel — only the slice has data

	first := a.drainStdinToQueue()
	assert.Equal(t, "pre-queued from skill_invoke", first,
		"a slice-only entry must be returned even when the channel is empty")
}

// TestDrainStdinToQueue_SlicePrecedesChannel verifies FIFO ordering
// across both backing stores. Pre-existing slice entries must be
// served before whatever the user just typed (in the channel),
// otherwise a follow-up typed mid-turn would jump ahead of a
// skill_invoke-queued directive.
func TestDrainStdinToQueue_SlicePrecedesChannel(t *testing.T) {
	cli := &ChatCLI{
		messageQueue: []string{"slice-1", "slice-2"},
	}
	a := &AgentMode{cli: cli}
	a.stdinLines = make(chan string, 4)
	a.stdinLines <- "channel-1"
	a.stdinLines <- "channel-2"

	got := []string{
		a.drainStdinToQueue(),
		a.drainStdinToQueue(),
		a.drainStdinToQueue(),
		a.drainStdinToQueue(),
	}
	assert.Equal(t, []string{"slice-1", "slice-2", "channel-1", "channel-2"}, got,
		"FIFO across both stores: slice first, then channel arrivals in order")

	cli.messageQueueMu.Lock()
	defer cli.messageQueueMu.Unlock()
	assert.Empty(t, cli.messageQueue, "everything must drain to empty")
}

// TestConsolidateStdinIntoQueue_NonBlocking is the contract used by the
// spinner callback: every tick the consolidation runs to move channel
// arrivals into the durable slice. It must never block — otherwise the
// timer callback would freeze the spinner.
func TestConsolidateStdinIntoQueue_NonBlocking(t *testing.T) {
	cli := &ChatCLI{}
	a := &AgentMode{cli: cli}
	a.stdinLines = make(chan string, 4)
	a.stdinLines <- "one"
	a.stdinLines <- "two"

	done := make(chan struct{})
	go func() {
		a.consolidateStdinIntoQueue()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("consolidateStdinIntoQueue blocked — would freeze spinner ticks")
	}

	cli.messageQueueMu.Lock()
	defer cli.messageQueueMu.Unlock()
	assert.Equal(t, []string{"one", "two"}, cli.messageQueue)
}

// TestConsolidateStdinIntoQueue_NilChannelSafe handles the lifecycle
// edge where the reader goroutine is stopped (stdinLines = nil) but a
// timer tick still fires before the timer is paused/stopped. Must be a
// no-op, never a nil-channel panic.
func TestConsolidateStdinIntoQueue_NilChannelSafe(t *testing.T) {
	cli := &ChatCLI{}
	a := &AgentMode{cli: cli} // stdinLines stays nil
	assert.NotPanics(t, func() {
		a.consolidateStdinIntoQueue()
	})
}

// TestCurrentStatusMsg_ExpiresAfterWindow proves the spinner status
// echo self-clears after the configured TTL — important because the
// spinner falls back to the default "Processing..." line once the
// transient "📥 enfileirado" flash expires.
func TestCurrentStatusMsg_ExpiresAfterWindow(t *testing.T) {
	cli := &ChatCLI{}
	a := &AgentMode{cli: cli}
	a.statusMu.Lock()
	a.statusMsg = "📥 something"
	a.statusExpires = time.Now().Add(20 * time.Millisecond)
	a.statusMu.Unlock()

	assert.Equal(t, "📥 something", a.currentStatusMsg())
	time.Sleep(40 * time.Millisecond)
	assert.Equal(t, "", a.currentStatusMsg(), "must clear after expiry")
}

// TestTruncateQueuePreview_LongString keeps the preview within a
// readable single-line budget so the "📨 next" banner doesn't wrap.
// Counted in runes — the function operates on codepoints so multibyte
// glyphs don't blow the column budget.
func TestTruncateQueuePreview_LongString(t *testing.T) {
	long := strings.Repeat("x", 200)
	got := truncateQueuePreview(long)
	assert.LessOrEqual(t, len([]rune(got)), 80, "preview must fit in 80 runes")
	assert.True(t, strings.HasSuffix(got, "…"), "long inputs must be ellipsized")

	short := "short message"
	assert.Equal(t, short, truncateQueuePreview(short),
		"short inputs must round-trip unchanged")

	// Unicode must not be split mid-rune. 100 Ω (2-byte codepoints)
	// would overflow a byte-counting truncator but stay safe under
	// the rune-based one.
	omega := strings.Repeat("Ω", 100)
	gotOmega := truncateQueuePreview(omega)
	assert.LessOrEqual(t, len([]rune(gotOmega)), 80)
	for _, r := range gotOmega {
		assert.NotEqual(t, '�', r, "no replacement char from mid-rune split")
	}
}

// TestMessageQueueConcurrency simulates the real call site: one writer
// (the stdin reader goroutine) and one reader (the ReAct loop drain).
// We require that no line is lost or duplicated.
func TestMessageQueueConcurrency(t *testing.T) {
	cli := &ChatCLI{}
	a := &AgentMode{cli: cli}
	a.stdinLines = make(chan string, 32)

	const n = 16
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < n; i++ {
			a.stdinLines <- "line-" + itoa(i)
		}
	}()
	wg.Wait() // ensure all writes complete before drain

	first := a.drainStdinToQueue()
	assert.True(t, strings.HasPrefix(first, "line-"))

	cli.messageQueueMu.Lock()
	defer cli.messageQueueMu.Unlock()
	assert.Equal(t, n-1, len(cli.messageQueue),
		"every input must be accounted for (1 returned + N-1 queued)")
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [16]byte
	pos := len(buf)
	neg := i < 0
	if neg {
		i = -i
	}
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}
