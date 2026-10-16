/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

// Package pulsetest records what code under test publishes on the
// process-wide pulse bus, so a test can assert that a subsystem reports
// itself to the live dashboard: which node, under which parent, with which
// attributes and outcome.
package pulsetest

import (
	"sync"
	"testing"
	"time"

	"github.com/diillson/chatcli/pkg/pulse"
)

// Recorder collects the events published after Record was called.
type Recorder struct {
	mu  sync.Mutex
	evs []pulse.Event
}

// Record turns the process-wide bus on for the rest of the test and starts
// collecting. The bus is restored and the subscription dropped at cleanup.
func Record(t testing.TB) *Recorder {
	t.Helper()
	bus := pulse.Default()
	was := bus.Enabled()
	bus.SetEnabled(true)
	ch, cancel := bus.Subscribe(1 << 14)
	r := &Recorder{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range ch {
			r.mu.Lock()
			r.evs = append(r.evs, ev)
			r.mu.Unlock()
		}
	}()
	t.Cleanup(func() {
		cancel()
		<-done
		if !was {
			bus.SetEnabled(false)
		}
	})
	return r
}

// Events returns a copy of what was collected so far.
func (r *Recorder) Events() []pulse.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]pulse.Event(nil), r.evs...)
}

// Match reports the collected events that satisfy pred.
func (r *Recorder) Match(pred func(pulse.Event) bool) []pulse.Event {
	var out []pulse.Event
	for _, ev := range r.Events() {
		if pred(ev) {
			out = append(out, ev)
		}
	}
	return out
}

// WaitFor waits up to two seconds for an event satisfying pred — the bus
// delivers asynchronously — and fails the test naming what it waited for.
func (r *Recorder) WaitFor(t testing.TB, what string, pred func(pulse.Event) bool) pulse.Event {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if m := r.Match(pred); len(m) > 0 {
			return m[0]
		}
		if time.Now().After(deadline) {
			t.Fatalf("pulse: no %s among %d events: %+v", what, len(r.Events()), r.Events())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Is matches events of a kind, name and phase ("" matches any phase).
func Is(kind pulse.Kind, name string, phase pulse.Phase) func(pulse.Event) bool {
	return func(ev pulse.Event) bool {
		return ev.Kind == kind && ev.Name == name && (phase == "" || ev.Phase == phase)
	}
}
