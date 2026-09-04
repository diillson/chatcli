/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/diillson/chatcli/models"
)

func waitUntil(t *testing.T, timeout time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return cond()
}

func TestMemoryWorker_BootDrainsTheQueuedBacklog(t *testing.T) {
	active := &scriptedClient{name: "claude", response: "NOTHING_NEW"}
	mw := newResilienceWorker(t, active)
	// A single segment queued by an earlier one-shot session: fewer
	// messages than the live-delta gate wants, so before the boot drain it
	// sat in the queue until enough REPL turns arrived (or forever for a
	// -p only user).
	if _, err := mw.persistPending([]models.Message{{Role: "user", Content: "fact"}, {Role: "assistant", Content: "noted"}}); err != nil {
		t.Fatal(err)
	}
	mw.start(context.Background())
	if !waitUntil(t, 5*time.Second, func() bool { return len(mw.pendingFiles()) == 0 }) {
		t.Fatalf("boot must drain the queue: %d segments left", len(mw.pendingFiles()))
	}
	if !mw.stopAndWait(5 * time.Second) {
		t.Fatal("stop must return once the loop and the pass finished")
	}
}

// blockingClient parks SendPrompt until released, to observe shutdown
// waiting for an in-flight pass.
type blockingClient struct {
	release chan struct{}
	started chan struct{}
	once    sync.Once
}

func (b *blockingClient) GetModelName() string { return "blocking" }
func (b *blockingClient) SendPrompt(ctx context.Context, _ string, _ []models.Message, _ int) (string, error) {
	b.once.Do(func() { close(b.started) })
	select {
	case <-b.release:
	case <-time.After(10 * time.Second):
	}
	return "NOTHING_NEW", nil
}

func TestMemoryWorker_StopWaitsForAnInFlightPassBounded(t *testing.T) {
	bc := &blockingClient{release: make(chan struct{}), started: make(chan struct{})}
	mw := newResilienceWorker(t, bc)
	if _, err := mw.persistPending([]models.Message{{Role: "user", Content: "fact"}, {Role: "assistant", Content: "noted"}}); err != nil {
		t.Fatal(err)
	}
	mw.start(context.Background())
	select {
	case <-bc.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the boot drain must start the extraction")
	}
	// Bounded: a hung provider call does not hold the exit.
	begin := time.Now()
	if mw.stopAndWait(200 * time.Millisecond) {
		t.Fatal("stop must report the pass still running")
	}
	if time.Since(begin) > 2*time.Second {
		t.Fatal("stop must return at the bound")
	}
	close(bc.release)
	if !waitUntil(t, 5*time.Second, func() bool { return len(mw.pendingFiles()) == 0 }) {
		t.Fatal("the released pass must still complete its write")
	}
}

func TestFlushMemoryBeforeCompaction_WatermarkIsRaceFree(t *testing.T) {
	active := &scriptedClient{name: "claude", response: "NOTHING_NEW"}
	mw := newResilienceWorker(t, active)
	c := mw.cli
	c.memWorker = mw
	c.history = []models.Message{{Role: "user", Content: "q"}, {Role: "assistant", Content: "a"}, {Role: "user", Content: "q2"}, {Role: "assistant", Content: "a2"}}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); mw.maybeExtract(context.Background()) }()
		go func() { defer wg.Done(); c.flushMemoryBeforeCompaction(context.Background()) }()
	}
	wg.Wait()
	if mw.watermark() != len(c.history) {
		t.Fatalf("watermark = %d", mw.watermark())
	}
	mw.stopAndWait(time.Second)
}

func TestShutdown_QueuesTheUnextractedTail(t *testing.T) {
	active := &scriptedClient{name: "claude", response: "NOTHING_NEW"}
	mw := newResilienceWorker(t, active)
	c := mw.cli
	c.memWorker = mw
	c.history = []models.Message{{Role: "user", Content: "last question"}, {Role: "assistant", Content: "last answer"}}
	c.queueMemoryBeforeCompaction()
	files := mw.pendingFiles()
	if len(files) != 1 {
		t.Fatalf("the tail must be queued for the next session: %d", len(files))
	}
	if st, err := os.Stat(files[0]); err != nil || st.Size() == 0 {
		t.Fatal("segment written")
	}
	if mw.watermark() != 2 {
		t.Fatal("watermark advanced past the queued tail")
	}
}
