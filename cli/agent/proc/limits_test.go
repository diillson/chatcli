//go:build !windows

/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package proc

import (
	"errors"
	"fmt"
	"regexp"
	"testing"
	"time"

	"go.uber.org/zap"
)

// plant tracks n entries for owner without running anything: the limits
// count entries, and filling a host ceiling with real processes would test
// the machine. Planted running entries are forgotten before CloseAll.
func plant(t *testing.T, s *Supervisor, owner string, state State, endedAgo time.Duration, n int) []string {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]string, 0, n)
	for i := 0; i < n; i++ {
		s.seq++
		id := fmt.Sprintf("p%d", s.seq)
		mp := &managedProcess{owner: owner, done: make(chan struct{}), info: Info{ID: id, State: state, Command: "planted"}}
		if state == StateExited {
			// Later plants are slightly newer, so "oldest" is the first.
			mp.info.Ended = time.Now().Add(-endedAgo).Add(time.Duration(i) * time.Millisecond)
			close(mp.done)
		}
		s.procs[id] = mp
		ids = append(ids, id)
	}
	if state == StateRunning {
		t.Cleanup(func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			for _, id := range ids {
				delete(s.procs, id)
			}
		})
	}
	return ids
}

func tracked(s *Supervisor, id string) bool {
	_, err := s.Status(id)
	return err == nil
}

func newLimitSupervisor(t *testing.T) *Supervisor {
	t.Helper()
	s := NewSupervisor(nil, zap.NewNop())
	t.Cleanup(s.CloseAll)
	return s
}

// The running limit is each owner's: one session at its limit is told so,
// with its own count, and neither another session nor the terminal is
// refused because of it.
func TestRunningLimitIsPerOwner(t *testing.T) {
	s := newLimitSupervisor(t)
	alice, bob := s.Table("session:alice"), s.Table("session:bob")
	plant(t, s, "session:alice", StateRunning, 0, maxRunning)

	_, err := alice.Start("sleep 30", "")
	if want := fmt.Sprintf("%d processes already running (limit %d) — stop one first (@proc stop)", maxRunning, maxRunning); err == nil || err.Error() != want {
		t.Fatalf("alice at her limit: got %v, want %q", err, want)
	}
	if _, err := bob.Start("sleep 30", ""); err != nil {
		t.Fatalf("bob must not be refused by alice's processes: %v", err)
	}
	if _, err := s.Start("sleep 30", ""); err != nil {
		t.Fatalf("the terminal must not be refused by a session's processes: %v", err)
	}
	// The terminal's own limit is the one it always had.
	plant(t, s, "", StateRunning, 0, maxRunning-1)
	if _, err := s.Start("sleep 30", ""); err == nil || err.Error() != fmt.Sprintf("%d processes already running (limit %d) — stop one first (@proc stop)", maxRunning, maxRunning) {
		t.Fatalf("the terminal at its limit: %v", err)
	}
}

// Past the host ceiling a session is refused without a number and without
// advice it cannot follow: it has nothing running to stop.
func TestHostRunningCeilingNamesNoOtherSession(t *testing.T) {
	s := newLimitSupervisor(t)
	for i := 0; i < maxRunningHost/maxRunning; i++ {
		plant(t, s, fmt.Sprintf("session:%d", i), StateRunning, 0, maxRunning)
	}
	_, err := s.Table("session:new").Start("sleep 30", "")
	if !errors.Is(err, errHostAtCapacity) {
		t.Fatalf("a start past the host ceiling: %v", err)
	}
	if msg := err.Error(); regexp.MustCompile(`\d|session|stop one`).MatchString(msg) {
		t.Fatalf("the refusal must not count, name or blame other sessions: %q", msg)
	}
}

// Eviction is each owner's too: a session filling its table evicts its own
// oldest exited entries and never another session's or the terminal's.
func TestEvictionNeverTakesAnotherOwnersEntries(t *testing.T) {
	s := newLimitSupervisor(t)
	alice := s.Table("session:alice")
	bobs := plant(t, s, "session:bob", StateExited, 3*time.Hour, 2) // older than anything of alice's
	terms := plant(t, s, "", StateExited, 3*time.Hour, 2)
	mine := plant(t, s, "session:alice", StateExited, time.Minute, maxRetained)

	for i := 0; i < 3; i++ {
		if _, err := alice.Start("true", ""); err != nil {
			t.Fatalf("alice start %d: %v", i, err)
		}
	}
	for i, id := range mine[:3] {
		if tracked(s, id) {
			t.Errorf("alice's oldest exited entry #%d must have been evicted", i)
		}
	}
	if !tracked(s, mine[3]) || len(alice.List()) != maxRetained {
		t.Errorf("alice keeps exactly her newest %d entries, has %d", maxRetained, len(alice.List()))
	}
	for _, id := range append(bobs, terms...) {
		if !tracked(s, id) {
			t.Errorf("%s belongs to another owner and must survive alice's activity", id)
		}
	}
}

// At the host ceiling a start makes room from the caller's own exited
// entries, then from what an idle session left behind — never from a
// session still in use, and never from the terminal. With no room to make,
// the start is refused and nothing is evicted.
func TestHostRetainedCeilingReclaimsOnlyIdleSessions(t *testing.T) {
	s := newLimitSupervisor(t)
	const each = maxRetained - 1 // under the per-owner limit: only the host ceiling acts
	recent := plant(t, s, "session:recent", StateExited, time.Minute, each)
	plant(t, s, "session:busy", StateExited, 3*time.Hour, each-1)
	plant(t, s, "session:busy", StateRunning, 0, 1) // old exits, but something still runs
	term := plant(t, s, "", StateExited, 3*time.Hour, each)
	idle := plant(t, s, "session:idle", StateExited, 3*time.Hour, maxRetainedHost-3*each)
	before := len(s.List())
	if before != maxRetainedHost {
		t.Fatalf("the test must sit at the host ceiling, has %d", before)
	}

	// A session with nothing of its own takes the idle session's oldest.
	if _, err := s.Table("session:new").Start("true", ""); err != nil {
		t.Fatalf("an idle session's leftovers must make room: %v", err)
	}
	if tracked(s, idle[0]) || !tracked(s, idle[1]) {
		t.Fatal("exactly the idle session's oldest exited entry must go")
	}
	// A session with exited entries of its own gives up its own oldest.
	if _, err := s.Table("session:recent").Start("true", ""); err != nil || tracked(s, recent[0]) {
		t.Fatalf("a session must evict its own entry first: %v", err)
	}
	if !tracked(s, idle[1]) {
		t.Fatal("the idle session's entry was taken although the caller had its own")
	}

	// Once nobody is idle there is no room to make for a newcomer.
	s.mu.Lock()
	for _, id := range idle[1:] {
		s.procs[id].info.Ended = time.Now()
	}
	s.mu.Unlock()
	if _, err := s.Table("session:late").Start("true", ""); !errors.Is(err, errHostAtCapacity) {
		t.Fatalf("no idle session and nothing of its own: %v", err)
	}
	for _, id := range append(append(term, recent[1:]...), idle[1:]...) {
		if !tracked(s, id) {
			t.Fatalf("%s was evicted for a session it does not belong to", id)
		}
	}
	if len(s.List()) != maxRetainedHost {
		t.Fatalf("the host must still hold %d entries, has %d", maxRetainedHost, len(s.List()))
	}
}
