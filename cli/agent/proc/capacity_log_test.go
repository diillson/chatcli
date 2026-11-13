//go:build !windows

/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package proc

import (
	"context"
	"errors"
	"strings"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// A headless server has no prompt to ask /proc list --all at. When a start
// is refused at a host ceiling, the client is told nothing about the
// others — and the server log says who holds the table.
func TestCapacityRefusalLogsTheHolders(t *testing.T) {
	core, logs := observer.New(zapcore.WarnLevel)
	s := NewSupervisor(nil, zap.New(core))
	t.Cleanup(s.CloseAll)
	for i := 0; i < maxRunningHost/maxRunning; i++ {
		plant(t, s, session(i), StateRunning, 0, maxRunning)
	}
	s.mu.Lock()
	for i := 0; i < maxRunningHost/maxRunning; i++ {
		s.handles[session(i)] = i + 1
	}
	s.mu.Unlock()

	_, err := s.Table("session:new").Start("sleep 30", "")
	if !errors.Is(err, errHostAtCapacity) {
		t.Fatalf("the start must be refused: %v", err)
	}
	entries := logs.FilterMessageSnippet("host at capacity").All()
	if len(entries) != 1 {
		t.Fatalf("one log line per refusal, got %d", len(entries))
	}
	fields := entries[0].ContextMap()
	holders, _ := fields["holders"].(string)
	for _, want := range []string{"o1 session:0 running=8 tracked=8", "o4 session:3 running=8 tracked=8"} {
		if !strings.Contains(holders, want) {
			t.Errorf("the log must name each holder with what it holds (%q missing): %q", want, holders)
		}
	}
	if fields["refused"] != "session:new" {
		t.Errorf("the log must say who was refused: %v", fields["refused"])
	}
	if strings.Contains(err.Error(), "session:0") || strings.Contains(err.Error(), "o1") {
		t.Errorf("none of that reaches the client: %q", err)
	}
}

// Owners get stable handles in the order they first start something, an
// owner's name is cleaned of control characters where it enters, and the
// terminal has neither a handle nor a name to clean.
func TestOwnerHandlesAndCleanNames(t *testing.T) {
	s := NewSupervisor(nil, zap.NewNop())
	t.Cleanup(s.CloseAll)
	hostile := OwnerFrom(WithSession(context.Background(), "a\x1b]0;pwned\x07b\u009b31m\n"))
	if hostile != "session:a]0;pwnedb31m" {
		t.Fatalf("control characters must be stripped where an owner enters: %q", hostile)
	}
	for _, owner := range []string{hostile, "session:two", ""} {
		if _, err := s.Table(owner).Start("true", ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Table(hostile).Start("true", ""); err != nil {
		t.Fatal(err)
	}
	if s.OwnerHandle(hostile) != "o1" || s.OwnerHandle("session:two") != "o2" || s.OwnerHandle("") != "" || s.OwnerHandle("session:never") != "" {
		t.Fatalf("handles: %q %q %q", s.OwnerHandle(hostile), s.OwnerHandle("session:two"), s.OwnerHandle(""))
	}
	if owner, ok := s.OwnerOfHandle("o2"); !ok || owner != "session:two" {
		t.Fatalf("o2 resolves to %q %v", owner, ok)
	}
	if _, ok := s.OwnerOfHandle("o9"); ok {
		t.Fatal("a handle never issued resolves to nothing")
	}
}
