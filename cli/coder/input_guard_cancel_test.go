/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package coder

import (
	"context"
	"testing"
	"time"

	"go.uber.org/zap"
)

// Once the prompt's context is done the debounce must not touch the input
// channel: a line sitting there belongs to whoever tears the reader down,
// and taking it here discarded it in silence. A select with both cases
// ready picks at random, so one pass proves nothing — many do.
func TestIntentDebounceLeavesTheChannelAloneOnceCancelled(t *testing.T) {
	g := NewInputGuard(zap.NewNop()).WithDebounceWindow(time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for i := 0; i < 500; i++ {
		ch := make(chan string, 1)
		ch <- "y"
		if n := g.IntentDebounce(ctx, ch); n != 0 || len(ch) != 1 {
			t.Fatalf("pass %d: a cancelled debounce discarded %d line(s)", i, n)
		}
	}
}
