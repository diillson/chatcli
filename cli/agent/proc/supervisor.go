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
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/diillson/chatcli/pkg/pulse"
	"go.uber.org/zap"
)

const (
	// maxRunning bounds the processes ONE owner runs concurrently; a session
	// juggling more than this is almost certainly leaking servers.
	maxRunning = 8

	// maxRetained bounds the entries tracked for ONE owner (running +
	// exited). Its oldest exited entries are evicted first so their logs
	// stay readable for a while without growing forever.
	maxRetained = 24

	// maxRunningHost / maxRetainedHost are the ceilings of the whole chatcli
	// process, whatever the number of sessions it serves: the per-owner
	// limits alone would let N sessions run 8N servers and hold 24N output
	// buffers. A lone owner never reaches them (both sit above its own
	// limits), so the terminal session behaves as it always did.
	maxRunningHost  = 32
	maxRetainedHost = 96

	// outputRingCap bounds each process's captured combined output.
	outputRingCap = 256 * 1024 // 256KiB

	// DefaultTailLines / MaxTailLines bound a logs read.
	DefaultTailLines = 100
	MaxTailLines     = 1000
)

// stopGrace is how long Stop waits after TERM before KILL; killWaitBound how
// long it then waits to reap. A descendant that left the process group but
// kept the output pipe blocks cmd.Wait while it lives: unbounded, Stop hung
// with it. Variables only so tests can shorten them.
var (
	stopGrace     = 5 * time.Second
	killWaitBound = 2 * time.Second

	// idleOwnerAfter is how long an owner must have had nothing running,
	// and nothing exiting, before the host ceiling may reclaim its exited
	// entries: sessions of a server are never closed, and what an abandoned
	// one left behind would otherwise hold the ceiling until restart.
	idleOwnerAfter = time.Hour
)

// errHostAtCapacity refuses a start at a process-wide ceiling. It names no
// count: the caller cannot see, and must not learn about, what the other
// sessions run.
var errHostAtCapacity = errors.New("this chatcli host is at its capacity for background processes — nothing of yours can be stopped to make room; try again later")

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
	PTY      bool      // started on a pseudo-terminal (accepts Stdin)
	// PipesAbandoned: killed, but a descendant outside the process group
	// still holds the output pipe, so Stop gave up waiting on it.
	PipesAbandoned bool
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

	// owner is the session that started the process ("" for the terminal).
	// Kept off Info: a snapshot must never tell one session about another.
	owner string

	outMu sync.Mutex
	out   []byte // ring-ish buffer, trimmed from the front at cap

	// ptmx is the pseudo-terminal master for PTY-started processes (nil
	// otherwise): the write side of Stdin and the read side of the ring.
	ptmx   *os.File
	ptmxMu sync.Mutex

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
	return s.startFor("", command, dir)
}

func (s *Supervisor) startFor(owner, command, dir string) (Info, error) {
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
	if err := s.admitLocked(owner); err != nil {
		return Info{}, err
	}

	cmd := shellCommand(command)
	if dir != "" {
		cmd.Dir = dir
	}
	setProcessGroup(cmd)

	s.seq++
	id := fmt.Sprintf("p%d", s.seq)
	mp := &managedProcess{
		info:  Info{ID: id, Command: command, Dir: dir, State: StateRunning, Started: time.Now()},
		cmd:   cmd,
		owner: owner,
		done:  make(chan struct{}),
	}
	cmd.Stdout = ringWriter{mp}
	cmd.Stderr = ringWriter{mp}

	if err := cmd.Start(); err != nil {
		return Info{}, fmt.Errorf("start: %w", err)
	}
	mp.info.PID = cmd.Process.Pid

	s.evictForLocked(owner)
	s.procs[id] = mp

	go func() {
		err := cmd.Wait()
		s.mu.Lock()
		mp.info.State = StateExited
		mp.info.Ended = time.Now()
		mp.info.ExitCode = exitCodeOf(err)
		exited := mp.info
		s.mu.Unlock()
		pulseProc(exited, pulse.PhaseEnd)
		close(mp.done)
	}()

	pulseProc(mp.info, pulse.PhaseStart)
	s.logger.Info("proc: started", zap.String("id", id), zap.Int("pid", mp.info.PID), zap.String("cmd", command))
	return mp.info, nil
}

// StartPTY launches command on a pseudo-terminal, so programs that demand a
// TTY (REPLs, debuggers, ssh, prompts) run for real and Stdin can drive
// them. Same vetting, limits and lifecycle as Start; Unix only.
func (s *Supervisor) StartPTY(command, dir string) (Info, error) {
	return s.startPTYFor("", command, dir)
}

func (s *Supervisor) startPTYFor(owner, command, dir string) (Info, error) {
	command = strings.TrimSpace(command)
	if command == "" {
		return Info{}, fmt.Errorf("empty command")
	}
	if !ptySupported {
		_, err := startWithPTY(nil)
		return Info{}, err
	}
	if s.validate != nil {
		if err := s.validate(command); err != nil {
			return Info{}, err
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.admitLocked(owner); err != nil {
		return Info{}, err
	}

	cmd := shellCommand(command)
	if dir != "" {
		cmd.Dir = dir
	}
	// No setProcessGroup here: the PTY start gives the child its own
	// session (Setsid + controlling terminal), which also makes pgid == pid
	// — the group-signal helpers in Stop keep working unchanged.

	ptmx, err := startWithPTY(cmd)
	if err != nil {
		return Info{}, fmt.Errorf("start pty: %w", err)
	}

	s.seq++
	id := fmt.Sprintf("p%d", s.seq)
	mp := &managedProcess{
		info:  Info{ID: id, Command: command, Dir: dir, State: StateRunning, Started: time.Now(), PTY: true},
		cmd:   cmd,
		owner: owner,
		ptmx:  ptmx,
		done:  make(chan struct{}),
	}
	mp.info.PID = cmd.Process.Pid

	s.evictForLocked(owner)
	s.procs[id] = mp

	// The master side is the only reader of the terminal's output.
	go func() {
		buf := make([]byte, 4096)
		w := ringWriter{mp}
		for {
			n, rerr := ptmx.Read(buf)
			if n > 0 {
				_, _ = w.Write(buf[:n])
			}
			if rerr != nil {
				return
			}
		}
	}()
	go func() {
		err := cmd.Wait()
		s.mu.Lock()
		mp.info.State = StateExited
		mp.info.Ended = time.Now()
		mp.info.ExitCode = exitCodeOf(err)
		exited := mp.info
		s.mu.Unlock()
		pulseProc(exited, pulse.PhaseEnd)
		mp.ptmxMu.Lock()
		_ = ptmx.Close()
		mp.ptmxMu.Unlock()
		close(mp.done)
	}()

	pulseProc(mp.info, pulse.PhaseStart)
	s.logger.Info("proc: started (pty)", zap.String("id", id), zap.Int("pid", mp.info.PID), zap.String("cmd", command))
	return mp.info, nil
}

// Stdin writes text to a PTY-started process's terminal; pressEnter appends
// carriage return (what a terminal's Enter key sends).
func (s *Supervisor) Stdin(id, text string, pressEnter bool) (Info, error) {
	return s.stdinFor(nil, id, text, pressEnter)
}

func (s *Supervisor) stdinFor(owner *string, id, text string, pressEnter bool) (Info, error) {
	s.mu.Lock()
	mp, ok := s.procs[id]
	if !ok || !mp.ownedBy(owner) {
		s.mu.Unlock()
		return Info{}, fmt.Errorf("unknown process id %q (see list)", id)
	}
	info := mp.info
	s.mu.Unlock()

	if !info.PTY {
		return info, fmt.Errorf("%s was not started with a pty — restart it via start with pty:true to drive its stdin", id)
	}
	if info.State != StateRunning {
		return info, fmt.Errorf("%s already exited (code %d)", id, info.ExitCode)
	}

	payload := text
	if pressEnter {
		payload += "\r"
	}
	mp.ptmxMu.Lock()
	defer mp.ptmxMu.Unlock()
	if mp.ptmx == nil {
		return info, fmt.Errorf("%s: terminal already closed", id)
	}
	if _, err := mp.ptmx.WriteString(payload); err != nil {
		return info, fmt.Errorf("write to %s: %w", id, err)
	}
	return info, nil
}

// Status returns the snapshot for one process.
func (s *Supervisor) Status(id string) (Info, error) {
	return s.statusFor(nil, id)
}

func (s *Supervisor) statusFor(owner *string, id string) (Info, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	mp, ok := s.procs[id]
	if !ok || !mp.ownedBy(owner) {
		return Info{}, s.unknownIDErrLocked(owner, id)
	}
	return mp.info, nil
}

// Logs returns the last tailLines lines of the process's combined output.
func (s *Supervisor) Logs(id string, tailLines int) (string, Info, error) {
	return s.logsFor(nil, id, tailLines)
}

func (s *Supervisor) logsFor(owner *string, id string, tailLines int) (string, Info, error) {
	s.mu.Lock()
	mp, ok := s.procs[id]
	if !ok || !mp.ownedBy(owner) {
		defer s.mu.Unlock()
		return "", Info{}, s.unknownIDErrLocked(owner, id)
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
	return s.stopFor(nil, id)
}

func (s *Supervisor) stopFor(owner *string, id string) (Info, error) {
	s.mu.Lock()
	mp, ok := s.procs[id]
	if !ok || !mp.ownedBy(owner) {
		defer s.mu.Unlock()
		return Info{}, s.unknownIDErrLocked(owner, id)
	}
	state := mp.info.State
	s.mu.Unlock()

	if state == StateRunning {
		terminateGroup(mp.cmd)
		select {
		case <-mp.done:
		case <-time.After(stopGrace):
			killGroup(mp.cmd)
			s.awaitKilled(mp, time.After(killWaitBound))
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	return mp.info, nil
}

// StopAsync requests Stop and returns the current snapshot at once, for
// callers that must not be held for the grace. The stop is bounded, and
// CloseAll still covers the process if the session ends first.
func (s *Supervisor) StopAsync(id string) (Info, error) {
	return s.stopAsyncFor(nil, id)
}

func (s *Supervisor) stopAsyncFor(owner *string, id string) (Info, error) {
	info, err := s.statusFor(owner, id)
	if err == nil && info.State == StateRunning {
		go func() { _, _ = s.stopFor(owner, id) }()
	}
	return info, err
}

// awaitKilled waits for a KILLed process to be reaped until bound fires,
// then records it as exited with its pipes abandoned (the reaper still
// fills in the real exit code if the pipes ever close).
func (s *Supervisor) awaitKilled(mp *managedProcess, bound <-chan time.Time) {
	select {
	case <-mp.done:
		return
	case <-bound:
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if mp.info.State == StateRunning {
		mp.info.State, mp.info.Ended, mp.info.ExitCode = StateExited, time.Now(), -1
		mp.info.PipesAbandoned = true
		s.logger.Warn("proc: killed, output pipes abandoned (a descendant left the process group)", zap.String("id", mp.info.ID))
	}
}

// Remove forgets an exited process (and its logs). Running processes must be
// stopped first.
func (s *Supervisor) Remove(id string) error {
	return s.removeFor(nil, id)
}

func (s *Supervisor) removeFor(owner *string, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	mp, ok := s.procs[id]
	if !ok || !mp.ownedBy(owner) {
		return s.unknownIDErrLocked(owner, id)
	}
	if mp.info.State == StateRunning {
		return fmt.Errorf("process %s is running — stop it first", id)
	}
	delete(s.procs, id)
	return nil
}

// List returns every tracked process, running first, then by recency.
func (s *Supervisor) List() []Info {
	return s.listFor(nil)
}

func (s *Supervisor) listFor(owner *string) []Info {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Info, 0, len(s.procs))
	for _, mp := range s.procs {
		if mp.ownedBy(owner) {
			out = append(out, mp.info)
		}
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
	bound := time.After(stopGrace + killWaitBound)
	for _, mp := range running {
		select {
		case <-mp.done:
		case <-deadline:
			killGroup(mp.cmd)
			s.awaitKilled(mp, bound)
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

// admitLocked says whether owner may start one more process. Its own limit
// is told with its own count, which it can act on. A host ceiling is told
// without numbers, and only when nothing of the caller's would make room.
func (s *Supervisor) admitLocked(owner string) error {
	if s.closed {
		return fmt.Errorf("supervisor closed")
	}
	running := 0
	for _, mp := range s.procs {
		if mp.owner == owner && mp.info.State == StateRunning {
			running++
		}
	}
	if running >= maxRunning {
		return fmt.Errorf("%d processes already running (limit %d) — stop one first (@proc stop)", running, maxRunning)
	}
	if s.runningLocked() >= maxRunningHost {
		return errHostAtCapacity
	}
	if len(s.procs) >= maxRetainedHost && s.evictableForLocked(owner) == "" {
		return errHostAtCapacity
	}
	return nil
}

// evictForLocked makes room for one more entry of owner. Under its own
// limit only owner's oldest EXITED entries go (running processes are never
// evicted), so one session's activity never costs another its logs. At the
// host ceiling it is still owner's entries first, then those an idle owner
// left behind.
func (s *Supervisor) evictForLocked(owner string) {
	for {
		tracked := 0
		for _, mp := range s.procs {
			if mp.owner == owner {
				tracked++
			}
		}
		if tracked < maxRetained && len(s.procs) < maxRetainedHost {
			return
		}
		id := s.oldestExitedLocked(owner)
		if id == "" && len(s.procs) >= maxRetainedHost {
			id = s.evictableForLocked(owner)
		}
		if id == "" {
			return // everything is running; the running limits bound this
		}
		delete(s.procs, id)
	}
}

// oldestExitedLocked is the id of owner's oldest exited entry, "" if none.
func (s *Supervisor) oldestExitedLocked(owner string) string {
	oldestID := ""
	var oldest time.Time
	for id, mp := range s.procs {
		if mp.owner != owner || mp.info.State != StateExited {
			continue
		}
		if oldestID == "" || mp.info.Ended.Before(oldest) {
			oldestID, oldest = id, mp.info.Ended
		}
	}
	return oldestID
}

// evictableForLocked is the entry a start by owner may evict at the host
// ceiling: owner's own oldest exited one, else the oldest exited entry of
// an IDLE owner — a named session with nothing running whose last exit is
// older than idleOwnerAfter. The terminal's entries and those of a session
// still in use are never taken.
func (s *Supervisor) evictableForLocked(owner string) string {
	if id := s.oldestExitedLocked(owner); id != "" {
		return id
	}
	lastSeen := map[string]time.Time{}
	busy := map[string]bool{}
	for _, mp := range s.procs {
		if mp.info.State == StateRunning {
			busy[mp.owner] = true
		} else if mp.info.Ended.After(lastSeen[mp.owner]) {
			lastSeen[mp.owner] = mp.info.Ended
		}
	}
	cutoff := time.Now().Add(-idleOwnerAfter)
	oldestID := ""
	var oldest time.Time
	for id, mp := range s.procs {
		if mp.owner == "" || busy[mp.owner] || !lastSeen[mp.owner].Before(cutoff) {
			continue
		}
		if oldestID == "" || mp.info.Ended.Before(oldest) {
			oldestID, oldest = id, mp.info.Ended
		}
	}
	return oldestID
}

// ownedBy reports whether a caller scoped to owner may see the process; a
// nil owner is the unscoped, process-wide view. Every lookup treats a
// process of another owner as absent, with the unknown-id answer: its
// existence is not disclosed either.
func (mp *managedProcess) ownedBy(owner *string) bool {
	return owner == nil || mp.owner == *owner
}

func (s *Supervisor) unknownIDErrLocked(owner *string, id string) error {
	ids := make([]string, 0, len(s.procs))
	for pid, mp := range s.procs {
		if mp.ownedBy(owner) {
			ids = append(ids, pid)
		}
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
