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

// Two sessions on one supervisor: each lists, reads and stops only what it
// started, and the other's id answers exactly like an id never issued.
func TestTableScopesEveryCallToItsOwner(t *testing.T) {
	s := NewSupervisor(nil, zap.NewNop())
	defer s.CloseAll()
	alice, bob := s.Table("session:alice"), s.Table("session:bob")
	a, errA := alice.Start(`sh -c 'echo alice-line; sleep 30'`, "")
	b, errB := bob.Start(`sh -c 'echo bob-line; sleep 30'`, "")
	// Supervisor.Start is the terminal's: a single-session process keeps
	// seeing its own processes exactly as before.
	term, errT := s.Start("sleep 30", "")
	if errA != nil || errB != nil || errT != nil || a.ID == b.ID {
		t.Fatalf("starts: %v %v %v (ids %s %s)", errA, errB, errT, a.ID, b.ID)
	}
	for tb, mine := range map[*Table]Info{alice: a, bob: b, s.Table(""): term} {
		if list := tb.List(); len(list) != 1 || list[0].ID != mine.ID {
			t.Fatalf("a table must list only its own process %s, got %+v", mine.ID, list)
		}
	}
	if all := s.List(); len(all) != 3 {
		t.Fatalf("the process-wide view must still see all three, got %d", len(all))
	}

	// Bob asking for alice's id (or the terminal's) gets, call by call, the
	// answer an id that was never issued gets.
	const never = "p999"
	unknown := tableErrors(bob, never)
	for _, other := range []string{a.ID, term.ID} {
		for call, got := range tableErrors(bob, other) {
			want := strings.ReplaceAll(unknown[call], never, other)
			if want == "<nil>" || got != want {
				t.Errorf("%s on another session's id: got %q, want the unknown-id answer %q", call, got, want)
			}
			if strings.Contains(strings.ReplaceAll(got, fmt.Sprintf("%q", other), ""), other) {
				t.Errorf("%s names the other session's id among the tracked ones: %q", call, got)
			}
		}
	}
	if info, err := alice.Status(a.ID); err != nil || info.State != StateRunning {
		t.Fatalf("alice's process must be untouched: %+v %v", info, err)
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
	if info, err := alice.Stop(a.ID); err != nil || info.State != StateExited {
		t.Fatalf("alice stop: %+v %v", info, err)
	}
	if info, err := bob.Status(b.ID); err != nil || info.State != StateRunning {
		t.Fatalf("stopping alice's process must leave bob's running: %+v %v", info, err)
	}
	if err := alice.Remove(a.ID); err != nil {
		t.Fatalf("alice remove: %v", err)
	}
	if info, err := bob.StopAsync(b.ID); err != nil || info.ID != b.ID {
		t.Fatalf("bob stop: %+v %v", info, err)
	}
	waitState(t, s, b.ID, StateExited, 30*time.Second)
}
