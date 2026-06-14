/*
 * ChatCLI - Memory extraction resilience tests.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/diillson/chatcli/cli/workspace"
	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

// scriptedClient fails the first failN calls, then answers with response.
type scriptedClient struct {
	name     string
	failN    int32
	calls    atomic.Int32
	response string
}

func (s *scriptedClient) GetModelName() string { return s.name }
func (s *scriptedClient) SendPrompt(_ context.Context, _ string, _ []models.Message, _ int) (string, error) {
	n := s.calls.Add(1)
	if n <= atomic.LoadInt32(&s.failN) {
		return "", errors.New(s.name + ": provider down")
	}
	return s.response, nil
}

func newResilienceWorker(t *testing.T, active client.LLMClient) *memoryWorker {
	t.Helper()
	c := &ChatCLI{
		logger:      zap.NewNop(),
		Client:      active,
		Provider:    "CLAUDEAI",
		memoryStore: workspace.NewMemoryStore(t.TempDir(), zap.NewNop()),
	}
	mw := newMemoryWorker(c)
	mw.pendingDir = filepath.Join(t.TempDir(), "pending")
	mw.lookupFallback = func(string) (client.LLMClient, error) { return nil, errors.New("none") }
	return mw
}

func TestCallExtraction_FallbackProviderServes(t *testing.T) {
	t.Setenv("CHATCLI_MEMORY_FALLBACK_PROVIDERS", "OPENAI")
	primary := &scriptedClient{name: "claude", failN: 99}
	fallback := &scriptedClient{name: "openai", response: "NOTHING_NEW"}

	mw := newResilienceWorker(t, primary)
	mw.lookupFallback = func(provider string) (client.LLMClient, error) {
		if provider != "OPENAI" {
			return nil, fmt.Errorf("unexpected provider %s", provider)
		}
		return fallback, nil
	}

	resp, err := mw.callExtraction(context.Background(), "prompt", nil)
	if err != nil {
		t.Fatalf("callExtraction: %v", err)
	}
	if resp != "NOTHING_NEW" {
		t.Fatalf("resp = %q, want fallback answer", resp)
	}
	if primary.calls.Load() == 0 || fallback.calls.Load() == 0 {
		t.Fatal("both primary and fallback must have been tried, in order")
	}
}

func TestCallExtraction_AllProvidersFail(t *testing.T) {
	t.Setenv("CHATCLI_MEMORY_FALLBACK_PROVIDERS", "")
	t.Setenv("CHATCLI_FALLBACK_PROVIDERS", "")
	mw := newResilienceWorker(t, &scriptedClient{name: "claude", failN: 99})
	if _, err := mw.callExtraction(context.Background(), "prompt", nil); err == nil {
		t.Fatal("must fail when every provider fails")
	}
}

func TestExtractionClients_DedupesActiveProvider(t *testing.T) {
	t.Setenv("CHATCLI_MEMORY_FALLBACK_PROVIDERS", "CLAUDEAI,OPENAI")
	mw := newResilienceWorker(t, &scriptedClient{name: "claude"})
	mw.lookupFallback = func(provider string) (client.LLMClient, error) {
		return &scriptedClient{name: provider}, nil
	}
	clients := mw.extractionClients()
	if len(clients) != 2 {
		t.Fatalf("expected active + 1 fallback (CLAUDEAI deduped), got %d", len(clients))
	}
	if clients[0].name != "CLAUDEAI" || clients[1].name != "OPENAI" {
		t.Fatalf("order wrong: %s, %s", clients[0].name, clients[1].name)
	}
}

func TestOnExtractionFailure_QueuesAndNotifies(t *testing.T) {
	mw := newResilienceWorker(t, &scriptedClient{name: "claude", failN: 99})
	segment := []models.Message{{Role: "user", Content: "almocei feijão hoje"}}

	mw.onExtractionFailure(errors.New("boom"), segment, 10)
	if got := len(mw.pendingFiles()); got != 1 {
		t.Fatalf("pending files = %d, want 1", got)
	}
	if mw.lastProcessedIdx != 10 {
		t.Fatalf("watermark = %d, want 10 (segment is durably queued)", mw.lastProcessedIdx)
	}
	if n := len(mw.cli.memNotices); n != 0 {
		t.Fatalf("no notice before threshold, got %d", n)
	}

	// Second consecutive failure crosses the threshold — exactly one notice.
	mw.onExtractionFailure(errors.New("boom"), segment, 12)
	mw.onExtractionFailure(errors.New("boom"), segment, 14)
	if n := len(mw.cli.memNotices); n != 1 {
		t.Fatalf("expected exactly 1 failure notice per streak, got %d", n)
	}
	if !strings.Contains(mw.cli.memNotices[0], "2") {
		t.Fatalf("notice should carry the streak count, got %q", mw.cli.memNotices[0])
	}

	// Success resets the streak so a future streak notifies again.
	mw.onExtractionSuccess(20)
	if mw.consecutiveFails != 0 || mw.failNoticeSent {
		t.Fatal("success must reset the failure streak")
	}
}

func TestDrainPending_ProcessesOldestAndStopsOnFailure(t *testing.T) {
	active := &scriptedClient{name: "claude", response: "NOTHING_NEW"}
	mw := newResilienceWorker(t, active)
	segment := []models.Message{{Role: "user", Content: "fato importante"}}

	for i := 0; i < 2; i++ {
		if _, err := mw.persistPending(segment); err != nil {
			t.Fatal(err)
		}
	}
	if got := mw.drainPending(context.Background()); got != 2 {
		t.Fatalf("drained = %d, want 2", got)
	}
	if got := len(mw.pendingFiles()); got != 0 {
		t.Fatalf("pending after drain = %d, want 0", got)
	}

	// A failing provider keeps the file for a later retry.
	atomic.StoreInt32(&active.failN, 99)
	active.calls.Store(0)
	if _, err := mw.persistPending(segment); err != nil {
		t.Fatal(err)
	}
	if got := mw.drainPending(context.Background()); got != 0 {
		t.Fatalf("drained = %d, want 0 while provider is down", got)
	}
	if got := len(mw.pendingFiles()); got != 1 {
		t.Fatalf("pending must survive a failed drain, got %d", got)
	}
}

func TestDrainPending_RemovesCorruptFiles(t *testing.T) {
	mw := newResilienceWorker(t, &scriptedClient{name: "claude", response: "NOTHING_NEW"})
	if err := os.MkdirAll(mw.pendingDir, 0o750); err != nil {
		t.Fatal(err)
	}
	corrupt := filepath.Join(mw.pendingDir, "seg-1.json")
	if err := os.WriteFile(corrupt, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	mw.drainPending(context.Background())
	if _, err := os.Stat(corrupt); !os.IsNotExist(err) {
		t.Fatal("corrupt pending file must be removed")
	}
}

func TestPersistPending_EnforcesCap(t *testing.T) {
	mw := newResilienceWorker(t, &scriptedClient{name: "claude"})
	segment := []models.Message{{Role: "user", Content: "x"}}
	for i := 0; i < pendingMaxFiles+5; i++ {
		if _, err := mw.persistPending(segment); err != nil {
			t.Fatal(err)
		}
	}
	if got := len(mw.pendingFiles()); got > pendingMaxFiles {
		t.Fatalf("pending files = %d, want <= %d", got, pendingMaxFiles)
	}
}
