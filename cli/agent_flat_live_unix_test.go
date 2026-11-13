//go:build !windows

/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/diillson/chatcli/cli/agent"
	"github.com/diillson/chatcli/cli/metrics"
	"go.uber.org/zap"
)

// ptyScreen stands in for the terminal: stdout is the slave side of a pty,
// so the turn timer ticks as it does for a user, and everything written is
// collected from the master side.
type ptyScreen struct {
	mu      sync.Mutex
	buf     strings.Builder
	changed chan struct{}
}

func openPTYScreen(t *testing.T) *ptyScreen {
	t.Helper()
	master, slave, err := pty.Open()
	if err != nil {
		t.Skipf("no pty available: %v", err)
	}
	s := &ptyScreen{changed: make(chan struct{}, 1)}
	orig := os.Stdout
	os.Stdout = slave
	done := make(chan struct{})
	go func() {
		defer close(done)
		chunk := make([]byte, 4096)
		for {
			n, readErr := master.Read(chunk)
			if n > 0 {
				s.mu.Lock()
				s.buf.Write(chunk[:n])
				s.mu.Unlock()
				select {
				case s.changed <- struct{}{}:
				default:
				}
			}
			if readErr != nil {
				return
			}
		}
	}()
	t.Cleanup(func() {
		os.Stdout = orig
		_ = slave.Close()
		_ = master.Close()
		<-done
	})
	return s
}

func (s *ptyScreen) text() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// waitFor blocks until what was written satisfies ok. It waits on the
// reader, not on the clock; the deadline only bounds a broken run.
func (s *ptyScreen) waitFor(t *testing.T, what string, ok func(string) bool) string {
	t.Helper()
	deadline := time.After(30 * time.Second)
	for {
		if got := s.text(); ok(got) {
			return got
		}
		select {
		case <-s.changed:
		case <-deadline:
			t.Fatalf("timed out waiting for %s; screen so far: %q", what, s.text())
		}
	}
}

// quietFor reports what was written during a window in which nothing is
// expected. It can only make a wrong program pass on a stalled machine,
// never make a right one fail, so it is not a synchronization point.
func (s *ptyScreen) quietFor(d time.Duration) string {
	before := len(s.text())
	<-time.After(d)
	return s.text()[before:]
}

func liveCallFixture(t *testing.T, style agent.UIStyle) (*AgentMode, *agent.UIRenderer) {
	t.Helper()
	useTheme(t, "onyx")
	r := agent.NewUIRendererWithStyle(zap.NewNop(), style)
	r.FlatToolQueue("@coder", "exec", `{"cmd":"exec","args":{"cmd":"make watch"}}`)
	r.FlatToolBegin()
	a := &AgentMode{cli: &ChatCLI{}, turnTimer: metrics.NewTimer(), policyAdapter: &workerPolicyAdapter{}}
	return a, r
}

const liveMark = "make watch" // only the live row of the fixture carries it

// The bug this guards: in the compact style a streaming tool never retired
// its live row, and a permission prompt raised by a worker during the call
// paused only the turn timer, so the row kept repainting over the prompt
// and the answer being typed. The row belongs to the turn timer now, so
// the prompt's pause takes it down and nothing repaints until its resume.
func TestLiveRowYieldsToThePermissionPrompt(t *testing.T) {
	screen := openPTYScreen(t)
	a, r := liveCallFixture(t, agent.UIStyleCompact)
	stop := a.startFlatToolLive(context.Background(), r)
	screen.waitFor(t, "the live row", func(s string) bool { return strings.Contains(s, liveMark) })

	// A streamed row arrives: the compact style hides it and the row
	// stays up, still owned by the timer.
	a.flatStreamRow(r, "building…", true)
	if !a.turnTimer.IsRunning() {
		t.Fatal("a hidden stream row took the live row down")
	}

	a.policyAdapter.pauseSpinner() // what the worker prompt does first
	if a.turnTimer.IsRunning() {
		t.Fatal("the prompt's pause did not reach the live row")
	}
	paused := screen.waitFor(t, "the erase", func(s string) bool { return strings.HasSuffix(s, "\r\033[K") })
	fmt.Print("Allow make watch? [y/n] > y")
	typed := screen.waitFor(t, "the prompt", func(s string) bool { return strings.HasSuffix(s, "> y") })
	if between := typed[len(paused):]; between != "Allow make watch? [y/n] > y" {
		t.Fatalf("something was painted between the pause and the prompt: %q", between)
	}
	if extra := screen.quietFor(350 * time.Millisecond); extra != "" {
		t.Fatalf("the row repainted while the prompt was up: %q", extra)
	}

	fmt.Println()
	a.policyAdapter.resumeSpinner()
	screen.waitFor(t, "the row after the prompt", func(s string) bool {
		return strings.Contains(s[len(typed):], liveMark)
	})

	stop()
	final := screen.waitFor(t, "the final erase", func(s string) bool { return strings.HasSuffix(s, "\r\033[K") })
	if a.turnTimer.IsRunning() {
		t.Fatal("the live row survived its stop")
	}
	if extra := screen.quietFor(250 * time.Millisecond); extra != "" {
		t.Fatalf("the row repainted after it was taken down: %q (screen ends %q)", extra, final[max(0, len(final)-40):])
	}
}

// A notice printed while a live row is up erases the row first, is written
// whole, and the row comes back under it.
func TestNoticeWhileLiveRowIsUp(t *testing.T) {
	screen := openPTYScreen(t)
	a, r := liveCallFixture(t, agent.UIStyleFull)
	stop := a.startFlatToolLive(context.Background(), r)
	defer stop()
	before := screen.waitFor(t, "the live row", func(s string) bool { return strings.Contains(s, liveMark) })

	a.announceParkedDraft("also update the changelog")
	after := screen.waitFor(t, "the notice and the row after it", func(s string) bool {
		i := strings.Index(s, "also update the changelog")
		return i >= 0 && strings.Contains(s[i:], liveMark)
	})
	at := strings.Index(after, "also update the changelog")
	lineStart := strings.LastIndex(after[:at], "\r\033[K")
	if lineStart < len(before)-len(liveMark) {
		t.Fatalf("the row was not erased between its last paint and the notice: %q", after)
	}
	// From the erase to the draft text there is the notice and nothing else.
	notice := after[lineStart+len("\r\033[K") : at]
	if !strings.HasPrefix(plainText(notice), "  • ") || strings.ContainsAny(notice, "\r↻") || strings.Contains(notice, liveMark) {
		t.Fatalf("the notice was written onto the live row: %q", notice)
	}
	if tail := after[at:]; !strings.Contains(tail, "\n") || strings.Index(tail, "\n") > strings.Index(tail, liveMark) {
		t.Fatalf("the row came back on the notice's line: %q", tail)
	}

	// A streamed row does the same, and from then on the live row is the
	// open end of the rail under the call's committed line.
	mark := len(screen.text())
	a.flatStreamRow(r, "compiling pkg/a", false)
	screen.waitFor(t, "the rail's live end", func(s string) bool {
		rest := plainText(s[mark:])
		i := strings.Index(rest, "compiling pkg/a")
		return i >= 0 && strings.Contains(rest[i:], "└ ↻")
	})
}

// While a task graph's own status holds the timer there is no second live
// row, and the no-op stop leaves that status alone.
func TestLiveRowHasOneOwner(t *testing.T) {
	screen := openPTYScreen(t)
	a, r := liveCallFixture(t, agent.UIStyleCompact)
	a.turnTimer.Start(context.Background(), func(time.Duration) { fmt.Print("\rGRAPH STATUS\033[K") })
	defer a.turnTimer.Stop()
	screen.waitFor(t, "the task graph status", func(s string) bool { return strings.Contains(s, "GRAPH STATUS") })

	stop := a.startFlatToolLive(context.Background(), r)
	stop()
	if !a.turnTimer.IsRunning() {
		t.Fatal("a tool's live row stopped the task graph status")
	}
	mark := len(screen.text())
	screen.waitFor(t, "the status after the no-op", func(s string) bool { return strings.Contains(s[mark:], "GRAPH STATUS") })
	if strings.Contains(screen.text(), liveMark) {
		t.Fatalf("two displays painted the same row: %q", screen.text())
	}
}

// Nobody watches a one-shot or unattended run, and captured output gets no
// escape sequence from the live row: the timeline is the final lines only.
func TestLiveRowIsForAttendedTerminalsOnly(t *testing.T) {
	screen := openPTYScreen(t)
	for name, mutate := range map[string]func(*AgentMode){
		"one-shot":   func(a *AgentMode) { a.isOneShot = true },
		"unattended": func(a *AgentMode) { a.cli.unattended = true },
		"no timer":   func(a *AgentMode) { a.turnTimer = nil },
	} {
		a, r := liveCallFixture(t, agent.UIStyleCompact)
		mutate(a)
		stop := a.startFlatToolLive(context.Background(), r)
		if a.turnTimer != nil && a.turnTimer.IsRunning() {
			t.Errorf("%s: the live row started", name)
		}
		stop()
	}
	if got := screen.text(); got != "" {
		t.Fatalf("a run nobody watches painted: %q", got)
	}

	a, r := liveCallFixture(t, agent.UIStyleCompact)
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	tty := os.Stdout
	os.Stdout = pw // a surface capturing the output mid-session
	stop := a.startFlatToolLive(context.Background(), r)
	a.withLiveRowPaused(func() {})
	stop()
	os.Stdout = tty
	_ = pw.Close()
	captured, _ := io.ReadAll(pr)
	if len(captured) != 0 {
		t.Fatalf("captured output received live-row bytes: %q", captured)
	}
}
