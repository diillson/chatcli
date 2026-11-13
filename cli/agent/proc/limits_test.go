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
		s.seq[owner]++
		id := fmt.Sprintf("p%d", s.seq[owner])
		mp := &managedProcess{owner: owner, done: make(chan struct{}), info: Info{ID: id, Owner: owner, State: state, Command: "planted"}}
		if state == StateExited {
			// Later plants are slightly newer, so "oldest" is the first.
			mp.info.Ended = time.Now().Add(-endedAgo).Add(time.Duration(i) * time.Millisecond)
			close(mp.done)
		}
		s.procs[procKey{owner, id}] = mp
		ids = append(ids, id)
	}
	if state == StateRunning {
		t.Cleanup(func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			for _, id := range ids {
				delete(s.procs, procKey{owner, id})
			}
		})
	}
	return ids
}

func tracked(s *Supervisor, owner, id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.procs[procKey{owner, id}]
	return ok
}

func countOf(s *Supervisor, owner string) int { return len(s.Table(owner).List()) }

func newLimitSupervisor(t *testing.T) *Supervisor {
	t.Helper()
	s := NewSupervisor(nil, zap.NewNop())
	t.Cleanup(s.CloseAll)
	return s
}

func session(i int) string { return fmt.Sprintf("session:%d", i) }

// The running limit is each owner's: one session at its limit is told so,
// with its own count, and neither another session nor the terminal is
// refused because of it.
func TestRunningLimitIsPerOwner(t *testing.T) {
	s := newLimitSupervisor(t)
	alice, bob := s.Table("session:alice"), s.Table("session:bob")
	plant(t, s, "session:alice", StateRunning, 0, maxRunning)
	limit := fmt.Sprintf("%d processes already running (limit %d) — stop one first (@proc stop)", maxRunning, maxRunning)

	if _, err := alice.Start("sleep 30", ""); err == nil || err.Error() != limit {
		t.Fatalf("alice at her limit: got %v, want %q", err, limit)
	}
	if _, err := bob.Start("sleep 30", ""); err != nil {
		t.Fatalf("bob must not be refused by alice's processes: %v", err)
	}
	if _, err := s.Start("sleep 30", ""); err != nil {
		t.Fatalf("the terminal must not be refused by a session's processes: %v", err)
	}
	// The terminal's own limit is the one it always had.
	plant(t, s, "", StateRunning, 0, maxRunning-1)
	if _, err := s.Start("sleep 30", ""); err == nil || err.Error() != limit {
		t.Fatalf("the terminal at its limit: %v", err)
	}
}

// At the running ceiling the sessions share, a session is refused without
// a number and without a word about the others; the terminal is not part
// of that ceiling in either direction.
func TestRunningCeilingIsTheSessionsOnly(t *testing.T) {
	s := newLimitSupervisor(t)
	plant(t, s, "", StateRunning, 0, maxRunning-1) // the terminal's do not count against sessions
	for i := 0; i < maxRunningHost/maxRunning-1; i++ {
		plant(t, s, session(i), StateRunning, 0, maxRunning)
	}
	last := s.Table("session:last")
	for i := 0; i < maxRunning; i++ {
		if _, err := last.Start("sleep 30", ""); err != nil {
			t.Fatalf("start %d under the ceiling was refused: %v", i, err)
		}
	}
	_, err := s.Table("session:new").Start("sleep 30", "")
	if !errors.Is(err, errHostAtCapacity) {
		t.Fatalf("a start past the running ceiling: %v", err)
	}
	if msg := err.Error(); regexp.MustCompile(`\d|session|other`).MatchString(msg) {
		t.Fatalf("the refusal must not count, name or mention other sessions: %q", msg)
	}
	if _, err := s.Start("sleep 30", ""); err != nil {
		t.Fatalf("the terminal must never be refused because of sessions: %v", err)
	}
}

// Eviction under an owner's own limit is that owner's alone: a session
// filling its table evicts its own oldest exited entries and never another
// session's or the terminal's, and the terminal never a session's.
func TestEvictionNeverTakesAnotherOwnersEntries(t *testing.T) {
	s := newLimitSupervisor(t)
	alice := s.Table("session:alice")
	bobs := plant(t, s, "session:bob", StateExited, 3*time.Hour, 2) // older than anything of alice's
	terms := plant(t, s, "", StateExited, 3*time.Hour, maxRetained)
	mine := plant(t, s, "session:alice", StateExited, time.Minute, maxRetained)

	for i := 0; i < 3; i++ {
		if _, err := alice.Start("true", ""); err != nil {
			t.Fatalf("alice start %d: %v", i, err)
		}
		if _, err := s.Start("true", ""); err != nil {
			t.Fatalf("terminal start %d: %v", i, err)
		}
	}
	for i := 0; i < 3; i++ {
		if tracked(s, "session:alice", mine[i]) || tracked(s, "", terms[i]) {
			t.Errorf("the oldest exited entry #%d of each filling owner must have been evicted", i)
		}
	}
	if countOf(s, "session:alice") != maxRetained || countOf(s, "") != maxRetained || !tracked(s, "session:alice", mine[3]) || !tracked(s, "", terms[3]) {
		t.Errorf("each keeps exactly its newest %d: alice %d, terminal %d", maxRetained, countOf(s, "session:alice"), countOf(s, ""))
	}
	for _, id := range bobs {
		if !tracked(s, "session:bob", id) {
			t.Errorf("bob's %s must survive the others' activity", id)
		}
	}
}

// The cases that used to wedge the host until restart. A few sessions
// holding the tracked ceiling must not refuse a newcomer for long, nor the
// terminal ever: an exited entry nobody has used for ten minutes makes
// room, whatever else its session is running.
func TestTrackedCeilingReclaimsUnusedExitedEntries(t *testing.T) {
	t.Run("four sessions each with one running and 23 exited", func(t *testing.T) {
		s := newLimitSupervisor(t)
		var oldest []string
		for i := 0; i < 4; i++ {
			ids := plant(t, s, session(i), StateExited, time.Duration(4-i)*time.Hour, maxRetained-1)
			plant(t, s, session(i), StateRunning, 0, 1)
			oldest = append(oldest, ids[0])
		}
		if _, err := s.Table("session:new").Start("true", ""); err != nil {
			t.Fatalf("a new session must be admitted: %v", err)
		}
		if tracked(s, session(0), oldest[0]) || !tracked(s, session(1), oldest[1]) {
			t.Fatal("exactly the least recently used exited entry must go")
		}
		if _, err := s.Start("true", ""); err != nil {
			t.Fatalf("the terminal must be admitted: %v", err)
		}
		for i := 0; i < 4; i++ {
			if running := s.Table(session(i)).List()[0]; running.State != StateRunning {
				t.Fatalf("a running process was evicted from %s", session(i))
			}
		}
	})
	t.Run("nothing running, four sessions exited 30 minutes ago", func(t *testing.T) {
		s := newLimitSupervisor(t)
		for i := 0; i < 4; i++ {
			plant(t, s, session(i), StateExited, 30*time.Minute, maxRetained)
		}
		if _, err := s.Table("session:new").Start("true", ""); err != nil {
			t.Fatalf("a new session must not wait out an hour: %v", err)
		}
	})
	t.Run("the terminal next to sessions at the ceiling", func(t *testing.T) {
		s := newLimitSupervisor(t)
		terms := plant(t, s, "", StateExited, 3*time.Hour, maxRetained)
		var theirs [][]string
		for i := 0; i < 4; i++ {
			theirs = append(theirs, plant(t, s, session(i), StateExited, time.Minute, maxRetained))
		}
		for i := 0; i < 3; i++ {
			if _, err := s.Start("sleep 30", ""); err != nil {
				t.Fatalf("terminal start %d refused: %v", i, err)
			}
		}
		for i, ids := range theirs {
			if countOf(s, session(i)) != maxRetained || !tracked(s, session(i), ids[0]) {
				t.Fatalf("the terminal's starts evicted an entry of %s", session(i))
			}
		}
		if tracked(s, "", terms[0]) || countOf(s, "") != maxRetained {
			t.Fatal("the terminal makes room from its own oldest entries, as always")
		}
		// And a session's start never takes the terminal's old entries,
		// though they are the least recently used on the host.
		if _, err := s.Table("session:new").Start("true", ""); !errors.Is(err, errHostAtCapacity) {
			t.Fatalf("every session entry was used a minute ago: %v", err)
		}
		if countOf(s, "") != maxRetained {
			t.Fatal("a session's start touched the terminal's table")
		}
	})
}

// "Unused" is the later of the exit and the last read: an old entry whose
// logs someone just read is not leftovers, and each kind of read counts.
// A caller's own entry goes before anyone else's, whatever its age.
func TestReadsKeepAnExitedEntryFromBeingReclaimed(t *testing.T) {
	s := newLimitSupervisor(t)
	re := regexp.MustCompile("x")
	reads := map[string]func(tb *Table, id string){
		"status": func(tb *Table, id string) { _, _ = tb.Status(id) },
		"logs":   func(tb *Table, id string) { _, _, _ = tb.Logs(id, 5) },
		"grep":   func(tb *Table, id string) { _, _, _, _, _ = tb.LogsMatching(id, re, 5) },
		"wait":   func(tb *Table, id string) { _, _ = tb.WaitFor(t.Context(), id, re, time.Millisecond) },
	}
	var read []string
	names := []string{"status", "logs", "grep", "wait"}
	for i, name := range names {
		ids := plant(t, s, session(i), StateExited, 3*time.Hour, maxRetained)
		reads[name](s.Table(session(i)), ids[0])
		read = append(read, ids[0])
	}
	// Every session's oldest entry was just read; their second oldest are
	// the least recently used now.
	if _, err := s.Table("session:new").Start("true", ""); err != nil {
		t.Fatalf("unused entries exist: %v", err)
	}
	for i, name := range names {
		if !tracked(s, session(i), read[i]) {
			t.Errorf("the entry just read with %s was reclaimed", name)
		}
	}
	if countOf(s, session(0)) != maxRetained-1 {
		t.Fatalf("the least recently used entry (session 0's second) must be the one taken")
	}
	// A session with exited entries of its own gives up its own oldest
	// (the one it just read included) before anyone else's.
	if _, err := s.Table(session(1)).Start("true", ""); err != nil || tracked(s, session(1), read[1]) {
		t.Fatalf("a caller's own oldest entry goes first: %v", err)
	}
	if countOf(s, session(2)) != maxRetained {
		t.Fatal("another session's entry was taken although the caller had its own")
	}
}
