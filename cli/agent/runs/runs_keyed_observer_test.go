/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package runs

import (
	"context"
	"sync"
	"testing"
)

// The registry used to hold ONE observer, owned by the hub bridge. A second
// consumer must now be able to attach without taking that slot away — losing
// it silently breaks cross-process /agents and remote cancel.
func TestKeyedObserversComposeWithTheDefaultSlot(t *testing.T) {
	reg := NewRegistry(8)
	var mu sync.Mutex
	var order []string
	note := func(who string) func(Info) {
		return func(Info) { mu.Lock(); order = append(order, who); mu.Unlock() }
	}

	reg.OnEvent(note("bridge"))
	reg.OnEventKeyed("pulse", note("pulse"))
	reg.OnEventKeyed("", note("ignored")) // empty key is rejected

	_, run := reg.Begin(context.Background(), Info{Kind: KindWorker, Agent: "coder"})
	if got := len(order); got != 2 {
		t.Fatalf("observers notified on Begin = %d, want 2 (%v)", got, order)
	}
	if order[0] != "bridge" || order[1] != "pulse" {
		t.Fatalf("delivery order = %v, want key order [default pulse]", order)
	}

	// Detaching the keyed consumer leaves the bridge attached...
	reg.OnEventKeyed("pulse", nil)
	order = nil
	run.SetTurn(1, 5)
	if len(order) != 1 || order[0] != "bridge" {
		t.Fatalf("after keyed detach: %v, want only the bridge", order)
	}

	// ...and detaching the bridge through the legacy call leaves keyed ones.
	reg.OnEventKeyed("pulse", note("pulse"))
	reg.OnEvent(nil)
	order = nil
	run.End(nil)
	if len(order) != 1 || order[0] != "pulse" {
		t.Fatalf("after OnEvent(nil): %v, want only pulse", order)
	}
}

// Re-registering the legacy slot replaces its previous owner (the behavior
// the bridge's stop/start relies on) and never touches keyed observers.
func TestOnEventStillReplacesItsOwnSlot(t *testing.T) {
	reg := NewRegistry(8)
	first, second, keyed := 0, 0, 0
	reg.OnEventKeyed("pulse", func(Info) { keyed++ })
	reg.OnEvent(func(Info) { first++ })
	reg.OnEvent(func(Info) { second++ })

	_, run := reg.Begin(context.Background(), Info{Kind: KindWorker})
	run.End(nil)
	if first != 0 || second != 2 || keyed != 2 {
		t.Fatalf("first=%d second=%d keyed=%d; want 0, 2, 2", first, second, keyed)
	}
}

// An observer may detach itself (or attach another) from inside its own
// callback: notify must not hold the hook lock while calling out.
func TestObserverMayReconfigureFromInsideCallback(t *testing.T) {
	reg := NewRegistry(8)
	calls := 0
	reg.OnEventKeyed("once", func(Info) {
		calls++
		reg.OnEventKeyed("once", nil)
	})
	_, run := reg.Begin(context.Background(), Info{Kind: KindWorker})
	run.SetTurn(1, 2)
	run.End(nil)
	if calls != 1 {
		t.Fatalf("self-detaching observer ran %d times, want 1", calls)
	}
}

func TestKeyedObserverOnNilRegistryIsSafe(t *testing.T) {
	var reg *Registry
	reg.OnEventKeyed("k", func(Info) {})
	reg.OnEvent(func(Info) {})
}
