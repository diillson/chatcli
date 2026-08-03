/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/diillson/chatcli/cli/agent/mail"
	"github.com/diillson/chatcli/server/hub"
	"go.uber.org/zap"
)

// startTestBridge opens a store handle on path and bridges it to a fresh
// registry, bypassing the process-wide single-bridge guard so one test can
// simulate several processes.
func startTestBridge(t *testing.T, ctx context.Context, path string, reg *mail.Registry) func() {
	t.Helper()
	store, err := hub.OpenSQLiteStore(ctx, path, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	mailBridgeActive.Store(false) // each simulated process gets its own bridge
	stop := startMailHubBridge(ctx, store, reg, zap.NewNop())
	return func() {
		stop()
		_ = store.Close()
	}
}

// waitFor polls cond until true or timeout.
func waitFor(t *testing.T, timeout time.Duration, cond func() bool) bool {
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

func TestMailHubBridgeCrossProcess(t *testing.T) {
	t.Setenv("CHATCLI_HUB_POLL_MS", "50")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	path := filepath.Join(t.TempDir(), "hub.db")

	regA := mail.NewRegistry(50) // "REPL"
	regB := mail.NewRegistry(50) // "gateway daemon"
	stopA := startTestBridge(t, ctx, path, regA)
	defer stopA()
	stopB := startTestBridge(t, ctx, path, regB)
	defer stopB()

	// A message sent in process A reaches process B's local queue.
	sent, err := regA.Send("user", "coder", "card-1", "prioritize the login fix")
	if err != nil {
		t.Fatal(err)
	}
	if !waitFor(t, 5*time.Second, func() bool { return len(regB.Peek("coder")) == 1 }) {
		t.Fatal("message did not propagate to the second process")
	}
	got := regB.Peek("coder")[0]
	if got.ID != sent.ID || got.CardID != "card-1" || got.From != "user" {
		t.Fatalf("payload mismatch: %+v vs %+v", got, sent)
	}
	// The sender's own queue still holds its copy (local enqueue), and the
	// echo from the store was deduplicated (exactly one copy).
	if n := len(regA.Peek("coder")); n != 1 {
		t.Fatalf("sender-side duplicate: %d copies", n)
	}
}

func TestMailHubBridgeAckStopsRedelivery(t *testing.T) {
	t.Setenv("CHATCLI_HUB_POLL_MS", "50")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	path := filepath.Join(t.TempDir(), "hub.db")

	regA := mail.NewRegistry(50)
	stopA := startTestBridge(t, ctx, path, regA)

	if _, err := regA.Send("reviewer", "coder", "", "fix tests"); err != nil {
		t.Fatal(err)
	}
	// Drain in A → persists an ack.
	if msgs := regA.Drain("coder"); len(msgs) != 1 {
		t.Fatalf("drain failed: %d", len(msgs))
	}
	// Give the ack append a moment, then boot a "new process" (hydration).
	time.Sleep(100 * time.Millisecond)
	stopA()

	regC := mail.NewRegistry(50)
	stopC := startTestBridge(t, ctx, path, regC)
	defer stopC()
	time.Sleep(300 * time.Millisecond)
	if n := len(regC.Peek("coder")); n != 0 {
		t.Fatalf("acked message was redelivered after restart: %d", n)
	}
}

func TestMailHubBridgeHydratesUnackedAfterRestart(t *testing.T) {
	t.Setenv("CHATCLI_HUB_POLL_MS", "50")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	path := filepath.Join(t.TempDir(), "hub.db")

	regA := mail.NewRegistry(50)
	stopA := startTestBridge(t, ctx, path, regA)
	if _, err := regA.Send("user", "orchestrator", "", "resume the report"); err != nil {
		t.Fatal(err)
	}
	// Wait for the persist to land, then "kill" the process WITHOUT draining.
	time.Sleep(100 * time.Millisecond)
	stopA()

	regB := mail.NewRegistry(50)
	stopB := startTestBridge(t, ctx, path, regB)
	defer stopB()
	if !waitFor(t, 5*time.Second, func() bool { return len(regB.Peek("orchestrator")) == 1 }) {
		t.Fatal("unacked message not hydrated after restart")
	}
}
