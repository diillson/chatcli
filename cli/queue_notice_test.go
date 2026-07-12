package cli

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/diillson/chatcli/ui/theme"
)

// captureQueueStdout is a local stdout capture for the queue-notice tests
// (named distinctly from the cost-render helper to avoid a collision once
// both slices land on main).
func captureQueueStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w
	defer func() { os.Stdout = old }()
	fn()
	_ = w.Close()
	out, _ := io.ReadAll(r)
	return string(out)
}

// TestExecutorQueuesTypeAheadWithNotices drives the executor's type-ahead
// branch: while a turn is executing, a plain message must be enqueued with
// the info notice, and an eleventh message must hit the queue-full warning.
func TestExecutorQueuesTypeAheadWithNotices(t *testing.T) {
	theme.SetProfile(theme.ProfileNoTTY)
	t.Cleanup(func() { theme.SetProfile(theme.DetectProfile()) })

	c := &ChatCLI{Client: &fakeClient{provider: "P", model: "m"}}
	c.isExecuting.Store(true)

	out := captureQueueStdout(t, func() { c.executor("mensagem digitada durante o turno") })
	if len(c.messageQueue) != 1 {
		t.Fatalf("queue len = %d, want 1", len(c.messageQueue))
	}
	if strings.TrimSpace(out) == "" {
		t.Fatal("queued notice not printed")
	}

	// Fill the queue to its cap and overflow it.
	for i := len(c.messageQueue); i < 10; i++ {
		c.messageQueue = append(c.messageQueue, "x")
	}
	out = captureQueueStdout(t, func() { c.executor("estourou a fila") })
	if len(c.messageQueue) != 10 {
		t.Fatalf("queue len = %d, want capped at 10", len(c.messageQueue))
	}
	if strings.TrimSpace(out) == "" {
		t.Fatal("queue-full warning not printed")
	}
}

// TestAnnounceQueueDrainBranches covers both drain announcements: with
// messages remaining and with an empty queue.
func TestAnnounceQueueDrainBranches(t *testing.T) {
	theme.SetProfile(theme.ProfileNoTTY)
	t.Cleanup(func() { theme.SetProfile(theme.DetectProfile()) })

	c := &ChatCLI{}
	c.messageQueue = []string{"next"}
	withRemaining := captureQueueStdout(t, func() { c.announceQueueDrain() })

	c.messageQueue = nil
	empty := captureQueueStdout(t, func() { c.announceQueueDrain() })

	if strings.TrimSpace(withRemaining) == "" || strings.TrimSpace(empty) == "" {
		t.Fatalf("drain notices missing: remaining=%q empty=%q", withRemaining, empty)
	}
	if withRemaining == empty {
		t.Fatal("the two drain branches must print different notices")
	}
}
