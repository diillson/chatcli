/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
/*
 * Background-process supervisor — the runtime behind the @proc agent tool.
 *
 * The coder engine's exec is one-shot: run, wait (bounded), return output.
 * That shape cannot express "start the dev server, keep it running, hit it
 * with a request, read its logs, shut it down" — the verify loop of real
 * development. The supervisor owns that lifecycle: it starts processes in
 * their own process group, captures bounded combined output in a ring
 * buffer, reports status, tails logs, and stops process trees (TERM →
 * grace → KILL). Everything dies with the session.
 *
 * Command safety is delegated to the SAME validator the agent's one-shot
 * exec uses (injected), so @proc never becomes a side door around policy.
 */
package proc

import (
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

const (
	// maxRunning bounds concurrently running processes; a session juggling
	// more than this is almost certainly leaking servers.
	maxRunning = 8

	// maxRetained bounds TOTAL tracked entries (running + exited). Oldest
	// exited entries are evicted first so their logs stay readable for a
	// while without growing forever.
	maxRetained = 24

	// outputRingCap bounds each process's captured combined output.
	outputRingCap = 256 * 1024 // 256KiB

	// stopGrace is how long Stop waits after TERM before escalating to KILL.
	stopGrace = 5 * time.Second

	// DefaultTailLines / MaxTailLines bound a logs read.
	DefaultTailLines = 100
	MaxTailLines     = 1000
)

// State is a process's lifecycle state.
type State string

const (
	StateRunning State = "running"
	StateExited  State = "exited"
)

// Info is a point-in-time snapshot of one supervised process.
type Info struct {
	ID       string
	PID      int
	Command  string
	Dir      string
	State    State
	ExitCode int // valid when State == StateExited
	Started  time.Time
	Ended    time.Time // zero while running
}

// Validator vets a command before it runs — injected so @proc shares the
// exact policy of the agent's one-shot exec.
type Validator func(cmd string) error

// Supervisor manages the session's background processes.
type Supervisor struct {
	logger   *zap.Logger
	validate Validator

	mu     sync.Mutex
	seq    int
	procs  map[string]*managedProcess
	closed bool
}

type managedProcess struct {
	info Info
	cmd  *exec.Cmd

	outMu sync.Mutex
	out   []byte // ring-ish buffer, trimmed from the front at cap

	done chan struct{}
}

// NewSupervisor builds a supervisor. A nil validator means "no vetting" —
// callers in production must inject the agent validator; tests may skip it.
func NewSupervisor(validate Validator, logger *zap.Logger) *Supervisor {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Supervisor{
		logger:   logger,
		validate: validate,
		procs:    map[string]*managedProcess{},
	}
}

// Start launches command (via the platform shell) in dir and returns the
// process id. The process joins its own process group so Stop can terminate
// the whole tree (dev servers fork workers).
func (s *Supervisor) Start(command, dir string) (Info, error) {
	command = strings.TrimSpace(command)
	if command == "" {
		return Info{}, fmt.Errorf("empty command")
	}
	if s.validate != nil {
		if err := s.validate(command); err != nil {
			return Info{}, err
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return Info{}, fmt.Errorf("supervisor closed")
	}
	if running := s.runningLocked(); running >= maxRunning {
		return Info{}, fmt.Errorf("%d processes already running (limit %d) — stop one first (@proc stop)", running, maxRunning)
	}

	cmd := shellCommand(command)
	if dir != "" {
		cmd.Dir = dir
	}
	setProcessGroup(cmd)

	s.seq++
	id := fmt.Sprintf("p%d", s.seq)
	mp := &managedProcess{
		info: Info{ID: id, Command: command, Dir: dir, State: StateRunning, Started: time.Now()},
		cmd:  cmd,
		done: make(chan struct{}),
	}
	cmd.Stdout = ringWriter{mp}
	cmd.Stderr = ringWriter{mp}

	if err := cmd.Start(); err != nil {
		return Info{}, fmt.Errorf("start: %w", err)
	}
	mp.info.PID = cmd.Process.Pid

	s.evictOldestExitedLocked()
	s.procs[id] = mp

	go func() {
		err := cmd.Wait()
		s.mu.Lock()
		mp.info.State = StateExited
		mp.info.Ended = time.Now()
		mp.info.ExitCode = exitCodeOf(err)
		s.mu.Unlock()
		close(mp.done)
	}()

	s.logger.Info("proc: started", zap.String("id", id), zap.Int("pid", mp.info.PID), zap.String("cmd", command))
	return mp.info, nil
}

// Status returns the snapshot for one process.
func (s *Supervisor) Status(id string) (Info, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	mp, ok := s.procs[id]
	if !ok {
		return Info{}, s.unknownIDErrLocked(id)
	}
	return mp.info, nil
}

// Logs returns the last tailLines lines of the process's combined output.
func (s *Supervisor) Logs(id string, tailLines int) (string, Info, error) {
	s.mu.Lock()
	mp, ok := s.procs[id]
	if !ok {
		defer s.mu.Unlock()
		return "", Info{}, s.unknownIDErrLocked(id)
	}
	info := mp.info
	s.mu.Unlock()

	if tailLines <= 0 {
		tailLines = DefaultTailLines
	}
	if tailLines > MaxTailLines {
		tailLines = MaxTailLines
	}
	mp.outMu.Lock()
	out := string(mp.out)
	mp.outMu.Unlock()
	return tailString(out, tailLines), info, nil
}

// Stop terminates the process tree: TERM to the group, a grace period, then
// KILL. Idempotent on exited processes.
func (s *Supervisor) Stop(id string) (Info, error) {
	s.mu.Lock()
	mp, ok := s.procs[id]
	if !ok {
		defer s.mu.Unlock()
		return Info{}, s.unknownIDErrLocked(id)
	}
	state := mp.info.State
	s.mu.Unlock()

	if state == StateRunning {
		terminateGroup(mp.cmd)
		select {
		case <-mp.done:
		case <-time.After(stopGrace):
			killGroup(mp.cmd)
			<-mp.done
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	return mp.info, nil
}

// Remove forgets an exited process (and its logs). Running processes must be
// stopped first.
func (s *Supervisor) Remove(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	mp, ok := s.procs[id]
	if !ok {
		return s.unknownIDErrLocked(id)
	}
	if mp.info.State == StateRunning {
		return fmt.Errorf("process %s is running — stop it first", id)
	}
	delete(s.procs, id)
	return nil
}

// List returns every tracked process, running first, then by recency.
func (s *Supervisor) List() []Info {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Info, 0, len(s.procs))
	for _, mp := range s.procs {
		out = append(out, mp.info)
	}
	sort.Slice(out, func(i, j int) bool {
		if (out[i].State == StateRunning) != (out[j].State == StateRunning) {
			return out[i].State == StateRunning
		}
		return out[i].Started.After(out[j].Started)
	})
	return out
}

// CloseAll stops every running process (used at session end) and marks the
// supervisor closed.
func (s *Supervisor) CloseAll() {
	s.mu.Lock()
	s.closed = true
	var running []*managedProcess
	for _, mp := range s.procs {
		if mp.info.State == StateRunning {
			running = append(running, mp)
		}
	}
	s.mu.Unlock()

	for _, mp := range running {
		terminateGroup(mp.cmd)
	}
	deadline := time.After(stopGrace)
	for _, mp := range running {
		select {
		case <-mp.done:
		case <-deadline:
			killGroup(mp.cmd)
			<-mp.done
		}
	}
}

// --- internals ---

func (s *Supervisor) runningLocked() int {
	n := 0
	for _, mp := range s.procs {
		if mp.info.State == StateRunning {
			n++
		}
	}
	return n
}

// evictOldestExitedLocked keeps total tracked entries under maxRetained by
// evicting the oldest EXITED entries (running processes are never evicted).
func (s *Supervisor) evictOldestExitedLocked() {
	for len(s.procs) >= maxRetained {
		oldestID := ""
		var oldest time.Time
		for id, mp := range s.procs {
			if mp.info.State != StateExited {
				continue
			}
			if oldestID == "" || mp.info.Ended.Before(oldest) {
				oldestID, oldest = id, mp.info.Ended
			}
		}
		if oldestID == "" {
			return // everything is running; the maxRunning gate bounds this
		}
		delete(s.procs, oldestID)
	}
}

func (s *Supervisor) unknownIDErrLocked(id string) error {
	ids := make([]string, 0, len(s.procs))
	for pid := range s.procs {
		ids = append(ids, pid)
	}
	sort.Strings(ids)
	if len(ids) == 0 {
		return fmt.Errorf("unknown process %q — nothing is tracked (start one with @proc start)", id)
	}
	return fmt.Errorf("unknown process %q — tracked: %s", id, strings.Join(ids, ", "))
}

// ringWriter appends to the process's bounded output buffer.
type ringWriter struct{ mp *managedProcess }

func (w ringWriter) Write(p []byte) (int, error) {
	w.mp.outMu.Lock()
	defer w.mp.outMu.Unlock()
	w.mp.out = append(w.mp.out, p...)
	if len(w.mp.out) > outputRingCap {
		cut := len(w.mp.out) - outputRingCap
		// Trim to the next newline so the buffer never starts mid-line.
		if nl := indexByteFrom(w.mp.out, cut, '\n'); nl >= 0 {
			cut = nl + 1
		}
		w.mp.out = append(w.mp.out[:0], w.mp.out[cut:]...)
	}
	return len(p), nil
}

func indexByteFrom(b []byte, from int, c byte) int {
	for i := from; i < len(b); i++ {
		if b[i] == c {
			return i
		}
	}
	return -1
}

// tailString returns the last n lines of s.
func tailString(s string, n int) string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return ""
	}
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// exitCodeOf extracts a process exit code from cmd.Wait's error.
func exitCodeOf(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}
