/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package pulse

import (
	"testing"
	"time"
)

func recv(t *testing.T, ch <-chan Event) Event {
	t.Helper()
	select {
	case ev, ok := <-ch:
		if !ok {
			t.Fatal("channel closed while waiting for an event")
		}
		return ev
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for an event")
	}
	return Event{}
}

func TestBusDisabledIsANoOp(t *testing.T) {
	b := New("t")
	ch, cancel := b.Subscribe(4)
	defer cancel()

	b.Emit(Event{Kind: KindTool, Phase: PhaseStart, ID: "x"})

	select {
	case ev := <-ch:
		t.Fatalf("disabled bus delivered %+v", ev)
	case <-time.After(50 * time.Millisecond):
	}
	if st := b.Stats(); st.Published != 0 || st.Dropped != 0 || st.Enabled {
		t.Fatalf("disabled bus counted activity: %+v", st)
	}
}

func TestBusStampsAndDelivers(t *testing.T) {
	b := New("inst-1")
	ch, cancel := b.Subscribe(4)
	defer cancel()
	b.SetEnabled(true)

	b.Emit(Event{Kind: KindLLM, Phase: PhaseStart, ID: "a"})
	b.Emit(Event{Kind: KindLLM, Phase: PhaseEnd, ID: "a"})

	first, second := recv(t, ch), recv(t, ch)
	if first.Seq != 1 || second.Seq != 2 {
		t.Fatalf("seqs = %d, %d; want 1, 2", first.Seq, second.Seq)
	}
	if first.Instance != "inst-1" || first.TS.IsZero() {
		t.Fatalf("event not stamped: %+v", first)
	}
	if b.Instance() != "inst-1" {
		t.Fatalf("Instance() = %q", b.Instance())
	}
}

func TestBusGeneratesInstanceToken(t *testing.T) {
	a, b := New(""), New("")
	if a.Instance() == "" || a.Instance() == b.Instance() {
		t.Fatalf("instance tokens not unique: %q vs %q", a.Instance(), b.Instance())
	}
}

// A subscriber that stops reading loses events; it must never stall the
// other subscribers or the emitter.
func TestBusSlowSubscriberDoesNotBlockOthers(t *testing.T) {
	b := New("t")
	_, cancelSlow := b.Subscribe(1) // never read
	defer cancelSlow()
	fast, cancelFast := b.Subscribe(64)
	defer cancelFast()
	b.SetEnabled(true)

	const n = 20
	for i := 0; i < n; i++ {
		b.Emit(Event{Kind: KindTool, Phase: PhasePoint, ID: "e"})
	}
	for i := 0; i < n; i++ {
		recv(t, fast)
	}
	if st := b.Stats(); st.SlowDropped != n-1 {
		t.Fatalf("SlowDropped = %d, want %d", st.SlowDropped, n-1)
	}
}

// With nothing draining the queue, Emit must return immediately and count
// the overflow instead of blocking the caller.
func TestBusEmitNeverBlocksWhenQueueIsFull(t *testing.T) {
	b := New("t")
	b.pumpOnce.Do(func() {}) // keep the pump from ever starting
	b.enabled.Store(true)

	done := make(chan struct{})
	go func() {
		for i := 0; i < queueSize+10; i++ {
			b.Emit(Event{Kind: KindTool, Phase: PhasePoint, ID: "e"})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Emit blocked on a full queue")
	}
	if st := b.Stats(); st.Published != queueSize || st.Dropped != 10 {
		t.Fatalf("published=%d dropped=%d, want %d and 10", st.Published, st.Dropped, queueSize)
	}
}

func TestBusSnapshotOnRisingEdgeOnly(t *testing.T) {
	b := New("t")
	b.RegisterSnapshotter("b-agents", func() []Event {
		return []Event{{Kind: KindAgent, Phase: PhaseStart, ID: "run-1", Status: StatusRunning}}
	})
	b.RegisterSnapshotter("a-mcp", func() []Event {
		return []Event{{Kind: KindMCP, Phase: PhaseStart, ID: "mcp:fs"}}
	})
	b.RegisterSnapshotter("", func() []Event { t.Error("empty key must be ignored"); return nil })

	ch, cancel := b.Subscribe(8)
	defer cancel()
	b.SetEnabled(true)
	b.SetEnabled(true) // already on: no second snapshot

	first, second := recv(t, ch), recv(t, ch)
	if first.ID != "mcp:fs" || second.ID != "run-1" {
		t.Fatalf("snapshot order = %q, %q; want key order", first.ID, second.ID)
	}
	if first.Attrs["snapshot"] != "true" {
		t.Fatalf("snapshot event not marked: %+v", first)
	}
	select {
	case ev := <-ch:
		t.Fatalf("second SetEnabled(true) re-emitted the snapshot: %+v", ev)
	case <-time.After(50 * time.Millisecond):
	}

	b.RegisterSnapshotter("a-mcp", nil)
	if got := b.Snapshot(); len(got) != 1 || got[0].ID != "run-1" {
		t.Fatalf("after removal Snapshot() = %+v", got)
	}
}

func TestBusSubscribeCancelIsIdempotent(t *testing.T) {
	b := New("t")
	ch, cancel := b.Subscribe(0)
	if cap(ch) != DefaultSubscriberBuffer {
		t.Fatalf("default buffer = %d", cap(ch))
	}
	cancel()
	cancel()
	if _, ok := <-ch; ok {
		t.Fatal("channel not closed by cancel")
	}
	if b.Stats().Subscribers != 0 {
		t.Fatal("subscriber not removed")
	}
	b.SetEnabled(true)
	b.Emit(Event{Kind: KindTool, Phase: PhasePoint, ID: "after-cancel"}) // must not panic
}

func TestNilBusIsSafe(t *testing.T) {
	var b *Bus
	b.Emit(Event{})
	b.SetEnabled(true)
	b.RegisterSnapshotter("k", func() []Event { return nil })
	if b.Enabled() || b.Instance() != "" || b.Snapshot() != nil || b.Stats() != (Stats{}) {
		t.Fatal("nil bus must read as empty and off")
	}
}

func TestDefaultBusHelpers(t *testing.T) {
	first, second := Default(), Default()
	if first != second {
		t.Fatal("Default() must be a singleton")
	}
	if Enabled() {
		t.Fatal("process-wide bus must start off")
	}
	Emit(Event{Kind: KindTool, Phase: PhasePoint, ID: "noop"}) // off: no-op, no panic
}

func TestEventWithAndTook(t *testing.T) {
	base := Event{ID: "x"}.With("a", "1")
	derived := base.With("b", "2").With("", "ignored").With("c", "")
	if len(base.Attrs) != 1 {
		t.Fatalf("With mutated its receiver: %+v", base.Attrs)
	}
	if len(derived.Attrs) != 2 || derived.Attrs["b"] != "2" {
		t.Fatalf("derived attrs = %+v", derived.Attrs)
	}
	if got := (Event{}).Took(1500 * time.Millisecond).DurMS; got != 1500 {
		t.Fatalf("Took(1.5s) = %d", got)
	}
	if got := (Event{}).Took(10 * time.Microsecond).DurMS; got != 1 {
		t.Fatalf("sub-millisecond Took = %d, want 1", got)
	}
	if got := (Event{}).Took(0).DurMS; got != 0 {
		t.Fatalf("Took(0) = %d", got)
	}
}
