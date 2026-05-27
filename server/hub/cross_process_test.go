/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package hub

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"github.com/diillson/chatcli/models"
)

// TestResolveSharesAcrossHandles proves the cross-process guarantee that local
// hub mode relies on: two store handles on the same database file (standing in
// for the CLI process and the gateway process) resolving the same principal
// concurrently must converge on ONE conversation, never fork two.
func TestResolveSharesAcrossHandles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hub.db")
	ctx := context.Background()

	a, err := OpenSQLiteStore(ctx, path, nil)
	if err != nil {
		t.Fatalf("open a: %v", err)
	}
	t.Cleanup(func() { _ = a.Close() })
	b, err := OpenSQLiteStore(ctx, path, nil)
	if err != nil {
		t.Fatalf("open b: %v", err)
	}
	t.Cleanup(func() { _ = b.Close() })

	const n = 16
	results := make([]string, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			store := a
			if i%2 == 1 {
				store = b
			}
			convID, err := store.Resolve(ctx, "edilson")
			if err != nil {
				t.Errorf("resolve: %v", err)
				return
			}
			results[i] = convID
		}(i)
	}
	wg.Wait()

	first := results[0]
	if first == "" {
		t.Fatal("empty conversation id")
	}
	for i, got := range results {
		if got != first {
			t.Fatalf("forked conversation at %d: %q != %q", i, got, first)
		}
	}

	// A turn written through one handle is visible through the other (shared log).
	if _, err := a.Append(ctx, models.ConversationEvent{ConvID: first, Principal: "edilson", Channel: "telegram", Role: models.ConvRoleUser, Content: "from phone"}); err != nil {
		t.Fatalf("append via a: %v", err)
	}
	got, err := b.Read(ctx, first, 0, 0)
	if err != nil {
		t.Fatalf("read via b: %v", err)
	}
	if len(got) != 1 || got[0].Content != "from phone" {
		t.Fatalf("cross-handle read mismatch: %+v", got)
	}
}
