//go:build !windows

/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package proc

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/diillson/chatcli/pkg/pulse"
	"go.uber.org/zap"
)

// tableErrors is the error each id-taking call of a table gives for id.
func tableErrors(tb *Table, id string) map[string]string {
	re := regexp.MustCompile("x")
	out := map[string]string{}
	set := func(name string, err error) { out[name] = fmt.Sprint(err) }
	_, err := tb.Status(id)
	set("status", err)
	_, _, err = tb.Logs(id, 5)
	set("logs", err)
	_, _, _, _, err = tb.LogsMatching(id, re, 5)
	set("grep", err)
	_, err = tb.WaitFor(context.Background(), id, re, time.Millisecond)
	set("wait", err)
	_, err = tb.Stdin(id, "x", true)
	set("stdin", err)
	_, err = tb.StopAsync(id)
	set("stop-async", err)
	_, err = tb.Stop(id)
	set("stop", err)
	set("remove", tb.Remove(id))
	return out
}

func waitTableState(t *testing.T, tb *Table, id string, want State) {
	t.Helper()
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		info, err := tb.Status(id)
		if err != nil || time.Now().After(deadline) {
			t.Fatalf("%s never reached %s: %+v %v", id, want, info, err)
		}
		if info.State == want {
			return
		}
	}
}

// Two sessions and the terminal on one supervisor: each lists, reads and
// stops only what it started, and an id it does not hold answers exactly
// like an id never issued.
func TestTableScopesEveryCallToItsOwner(t *testing.T) {
	s := NewSupervisor(nil, zap.NewNop())
	defer s.CloseAll()
	alice, bob := s.Table("session:alice"), s.Table("session:bob")
	a, errA := alice.Start(`sh -c 'echo alice-line; sleep 30'`, "")
	a2, errA2 := alice.Start("sleep 30", "")
	b, errB := bob.Start(`sh -c 'echo bob-line; sleep 30'`, "")
	// Supervisor.Start is the terminal's: a single-session process keeps
	// seeing its own processes exactly as before.
	term, errT := s.Start("sleep 30", "")
	if errA != nil || errA2 != nil || errB != nil || errT != nil {
		t.Fatalf("starts: %v %v %v %v", errA, errA2, errB, errT)
	}
	for tb, want := range map[*Table]string{alice: "p1,p2", bob: "p1", s.Table(""): "p1"} {
		var ids []string
		for _, info := range tb.List() {
			if info.Owner != tb.owner {
				t.Fatalf("a table listed another owner's process: %+v", info)
			}
			ids = append(ids, info.ID)
		}
		if got := sortedIDs(ids); got != want {
			t.Fatalf("table of %q lists %s, want %s", tb.owner, got, want)
		}
	}
	if all := s.List(); len(all) != 4 {
		t.Fatalf("the process-wide view must still see all four, got %d", len(all))
	}

	// Bob asking for an id only alice holds gets, call by call, the answer
	// an id that was never issued gets, and never learns of her ids.
	const never = "p999"
	unknown := tableErrors(bob, never)
	for call, got := range tableErrors(bob, a2.ID) {
		want := strings.ReplaceAll(unknown[call], never, a2.ID)
		if want == "<nil>" || got != want {
			t.Errorf("%s on an id of another session: got %q, want the unknown-id answer %q", call, got, want)
		}
	}
	// The same id in two tables is two processes: bob stopping his p1
	// leaves alice's p1 and the terminal's p1 alone.
	if info, err := bob.Stop(b.ID); err != nil || info.State != StateExited {
		t.Fatalf("bob stop: %+v %v", info, err)
	}
	for name, tb := range map[string]*Table{"alice": alice, "terminal": s.Table("")} {
		if info, err := tb.Status("p1"); err != nil || info.State != StateRunning {
			t.Fatalf("%s's p1 must be untouched by bob's stop of his p1: %+v %v", name, info, err)
		}
	}
	if info, err := s.Status(term.ID); err != nil || info.Owner != "" {
		t.Fatalf("an id on the Supervisor itself is the terminal's: %+v %v", info, err)
	}

	// Each owner reads and stops its own.
	if r, err := alice.WaitFor(context.Background(), a.ID, regexp.MustCompile("alice-line"), 20*time.Second); err != nil || !r.Matched {
		t.Fatalf("alice wait: %+v %v", r, err)
	}
	if logs, _, err := alice.Logs(a.ID, 5); err != nil || !strings.Contains(logs, "alice-line") {
		t.Fatalf("alice logs: %q %v", logs, err)
	}
	if lines, matched, _, _, err := alice.LogsMatching(a.ID, regexp.MustCompile("alice"), 5); err != nil || matched != 1 || len(lines) != 1 {
		t.Fatalf("alice grep: %v %d %v", lines, matched, err)
	}
	if info, err := alice.StopAsync(a.ID); err != nil || info.ID != a.ID {
		t.Fatalf("alice stop: %+v %v", info, err)
	}
	waitTableState(t, alice, a.ID, StateExited)
	if err := alice.Remove(a.ID); err != nil {
		t.Fatalf("alice remove: %v", err)
	}
}

func sortedIDs(ids []string) string {
	out := append([]string(nil), ids...)
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return strings.Join(out, ",")
}

// Ids are numbered per owner: a session's ids show nothing of how many
// processes the others started.
func TestIDsAreNumberedPerOwner(t *testing.T) {
	s := NewSupervisor(nil, zap.NewNop())
	defer s.CloseAll()
	a1, _ := s.Table("session:a").Start("true", "")
	b1, _ := s.Table("session:b").Start("true", "")
	b2, _ := s.Table("session:b").Start("true", "")
	a2, _ := s.Table("session:a").Start("true", "")
	t1, _ := s.Start("true", "")
	if got := a1.ID + a2.ID + b1.ID + b2.ID + t1.ID; got != "p1p2p1p2p1" {
		t.Fatalf("ids leak other owners' activity: %s", got)
	}
}

// Telemetry tags a session's process with its owner and qualifies its node
// id; the terminal's events are what they always were.
func TestPulseEventsCarryTheOwner(t *testing.T) {
	term := pulseProcEvent(Info{ID: "p1", Command: "node server.js"}, pulse.PhaseStart)
	if term.ID != "proc:p1" || term.Attrs[PulseOwnerAttr] != "" {
		t.Fatalf("the terminal's event changed: %+v", term)
	}
	sess := pulseProcEvent(Info{ID: "p1", Owner: "session:a", Command: "node server.js"}, pulse.PhaseStart)
	if sess.ID != "proc:session:a/p1" || sess.Attrs[PulseOwnerAttr] != "session:a" {
		t.Fatalf("a session's event must be tagged and distinct from the terminal's p1: %+v", sess)
	}
	s := NewSupervisor(nil, zap.NewNop())
	defer s.CloseAll()
	if _, err := s.Table("session:a").Start("sleep 30", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Start("sleep 30", ""); err != nil {
		t.Fatal(err)
	}
	owners := map[string]int{}
	for _, ev := range s.PulseSnapshot() {
		owners[ev.Attrs[PulseOwnerAttr]]++
	}
	if owners["session:a"] != 1 || owners[""] != 1 {
		t.Fatalf("the snapshot stays process-wide, tagged per owner: %v", owners)
	}
}
