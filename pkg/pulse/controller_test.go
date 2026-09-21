/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package pulse

import (
	"context"
	"testing"
	"time"
)

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// The whole point of the lease: a process with no prompt starts recording
// when a dashboard appears and goes quiet again when it leaves.
func TestControllerFollowsTheLease(t *testing.T) {
	root := t.TempDir()
	bus := New("proc-1")
	bus.RegisterSnapshotter("agents", func() []Event {
		return []Event{{Kind: KindAgent, Phase: PhaseStart, ID: "run-live", Status: StatusRunning}}
	})
	c := Start(context.Background(), Options{Bus: bus, Root: root, Meta: Meta{Surface: "acp", PID: 7}, Poll: 20 * time.Millisecond})
	defer c.Close()

	time.Sleep(60 * time.Millisecond)
	if c.Recording() || bus.Enabled() {
		t.Fatal("recording without a lease")
	}
	bus.Emit(Event{Kind: KindTool, Phase: PhasePoint, ID: "before-lease"}) // dropped: bus is off

	if err := RenewLease(root, "dash", time.Minute); err != nil {
		t.Fatal(err)
	}
	c.Wake()
	waitFor(t, "recording to start", c.Recording)

	bus.Emit(Event{Kind: KindTool, Phase: PhaseStart, ID: "t1", Name: "@read"})
	var got []Event
	waitFor(t, "events to reach the spool", func() bool {
		got, _, _ = ReadSince(root, "proc-1", 0, 0)
		return len(got) >= 2
	})
	if got[0].ID != "run-live" || got[0].Attrs["snapshot"] != "true" {
		t.Fatalf("first spooled event must be the snapshot, got %+v", got[0])
	}
	if got[1].ID != "t1" {
		t.Fatalf("second event = %+v", got[1])
	}
	metas := ListInstances(root)
	if len(metas) != 1 || metas[0].Surface != "acp" || metas[0].PID != 7 || !metas[0].Alive(time.Now()) {
		t.Fatalf("meta = %+v", metas)
	}

	ReleaseLease(root)
	waitFor(t, "recording to stop", func() bool { return !c.Recording() && !bus.Enabled() })
	if m := ListInstances(root); len(m) != 1 || !m[0].Ended {
		t.Fatalf("spool not marked ended: %+v", m)
	}

	// A second dashboard later: same instance dir, sequence keeps growing.
	_ = RenewLease(root, "dash-2", time.Minute)
	c.Wake()
	waitFor(t, "recording to restart", c.Recording)
	bus.Emit(Event{Kind: KindTool, Phase: PhaseEnd, ID: "t1"})
	waitFor(t, "post-restart event", func() bool {
		evs, _, _ := ReadSince(root, "proc-1", got[len(got)-1].Seq, 0)
		for _, ev := range evs {
			if ev.ID == "t1" && ev.Phase == PhaseEnd {
				return true
			}
		}
		return false
	})
}

func TestControllerForcedRecordsWithoutLeaseAndFlushesOnClose(t *testing.T) {
	root := t.TempDir()
	bus := New("proc-f")
	var reported []error
	c := Start(context.Background(), Options{Bus: bus, Root: root, Forced: true, Poll: time.Hour,
		OnError: func(err error) { reported = append(reported, err) }})
	waitFor(t, "forced recording", c.Recording)
	if c.Root() != root {
		t.Fatalf("Root() = %q", c.Root())
	}

	for i := 0; i < 50; i++ {
		bus.Emit(Event{Kind: KindLLM, Phase: PhasePoint, ID: "burst"})
	}
	waitFor(t, "the pump to hand the burst over", func() bool { return len(bus.queue) == 0 })
	c.Close()

	got, _, err := ReadSince(root, "proc-f", 0, 0)
	if err != nil || len(got) != 50 {
		t.Fatalf("after Close spool has %d events (err %v), want all 50", len(got), err)
	}
	if bus.Enabled() {
		t.Fatal("Close must turn the bus off")
	}
	if len(reported) != 0 {
		t.Fatalf("unexpected spool errors: %v", reported)
	}
}

func TestControllerReportsSpoolFailureWithoutDying(t *testing.T) {
	bus := New("../bad") // instance that OpenSpool refuses
	errs := make(chan error, 4)
	c := Start(context.Background(), Options{Bus: bus, Root: t.TempDir(), Forced: true, Poll: 10 * time.Millisecond,
		OnError: func(err error) {
			select {
			case errs <- err:
			default:
			}
		}})
	defer c.Close()
	select {
	case <-errs:
	case <-time.After(2 * time.Second):
		t.Fatal("spool failure was not reported")
	}
	if c.Recording() || bus.Enabled() {
		t.Fatal("must stay off when the spool cannot open")
	}
}

func TestControllerSetSurfaceReachesMeta(t *testing.T) {
	root := t.TempDir()
	c := Start(context.Background(), Options{Bus: New("proc-s"), Root: root, Forced: true, Poll: time.Hour, Meta: Meta{Surface: "repl"}})
	defer c.Close()
	waitFor(t, "recording", c.Recording)
	c.SetSurface("")
	c.SetSurface("acp")
	waitFor(t, "surface rename", func() bool {
		m := ListInstances(root)
		return len(m) == 1 && m[0].Surface == "acp"
	})
}

func TestNilControllerIsSafe(t *testing.T) {
	var c *Controller
	c.SetSurface("x")
	c.Wake()
	c.Close()
	if c.Recording() || c.Root() != "" {
		t.Fatal("nil controller must read as off")
	}
}

func TestStartResolvesDefaults(t *testing.T) {
	c := Start(context.Background(), Options{Poll: time.Hour})
	if c == nil {
		t.Skip("no home directory in this environment")
	}
	defer c.Close()
	if c.Root() == "" || c.bus != Default() {
		t.Fatalf("defaults not resolved: root=%q", c.Root())
	}
}
