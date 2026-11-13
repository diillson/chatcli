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

	// maxRunningHost / maxRetainedHost are the ceilings the NAMED owners
	// (the sessions a server process serves) share: the per-owner limits
	// alone would let N sessions run 8N servers and hold 24N output
	// buffers. The terminal — the empty owner, the local operator — is
	// outside both: its 8 and 24 are its own, it is never refused or
	// evicted because of sessions, and it does not count against them.
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

	// unusedEntryAfter is how long an EXITED entry must have gone unused —
	// since it exited and since it was last read (status, logs, grep, wait)
	// — before a start by another session may evict it at the tracked
	// ceiling. Sessions of a server are never closed, and what they leave
	// behind would otherwise hold the ceiling until restart; an entry
	// someone is still reading is not leftovers.
	unusedEntryAfter = 10 * time.Minute
)

// errHostAtCapacity refuses a start at a host ceiling. It names no count
// and no session: the caller cannot see, and must not learn about, what
// the others run.
var errHostAtCapacity = errors.New("this chatcli host is at its capacity for background processes — stop or remove one of your own if you have any, or try again later")

// State is a process's lifecycle state.
type State string

const (
	StateRunning State = "running"
	StateExited  State = "exited"
)

// Info is a point-in-time snapshot of one supervised process.
type Info struct {
	ID string
	// Owner is the session that started the process, "" for the terminal.
	// Ids are numbered per owner, so an id names a process only together
	// with it. Views scoped to one owner never print it.
	Owner    string
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

	mu sync.Mutex
	// seq numbers ids per owner: one sequence for all would let a session
	// count the others' starts by the gaps in its own ids.
	seq    map[string]int
	procs  map[procKey]*managedProcess
	closed bool
}

// procKey names one tracked process: ids repeat across owners.
type procKey struct{ owner, id string }

type managedProcess struct {
	info Info
	cmd  *exec.Cmd

	// owner is the session that started the process ("" for the terminal).
	owner string
	// lastRead is when status, logs, grep or wait last looked at it
	// (guarded by Supervisor.mu); with the exit time it says how long the
	// entry has gone unused.
	lastRead time.Time

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
		seq:      map[string]int{},
		procs:    map[procKey]*managedProcess{},
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

	s.seq[owner]++
	id := fmt.Sprintf("p%d", s.seq[owner])
	mp := &managedProcess{
		info:  Info{ID: id, Owner: owner, Command: command, Dir: dir, State: StateRunning, Started: time.Now()},
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
	s.procs[procKey{owner, id}] = mp

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

	s.seq[owner]++
	id := fmt.Sprintf("p%d", s.seq[owner])
	mp := &managedProcess{
		info:  Info{ID: id, Owner: owner, Command: command, Dir: dir, State: StateRunning, Started: time.Now(), PTY: true},
		cmd:   cmd,
		owner: owner,
		ptmx:  ptmx,
		done:  make(chan struct{}),
	}
	mp.info.PID = cmd.Process.Pid

	s.evictForLocked(owner)
	s.procs[procKey{owner, id}] = mp

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
	return s.stdinFor("", id, text, pressEnter)
}

func (s *Supervisor) stdinFor(owner string, id, text string, pressEnter bool) (Info, error) {
	s.mu.Lock()
	mp, ok := s.procs[procKey{owner, id}]
	if !ok {
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
	return s.statusFor("", id)
}

func (s *Supervisor) statusFor(owner string, id string) (Info, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	mp, ok := s.procs[procKey{owner, id}]
	if !ok {
		return Info{}, s.unknownIDErrLocked(owner, id)
	}
	mp.lastRead = time.Now()
	return mp.info, nil
}

// Logs returns the last tailLines lines of the process's combined output.
func (s *Supervisor) Logs(id string, tailLines int) (string, Info, error) {
	return s.logsFor("", id, tailLines)
}

func (s *Supervisor) logsFor(owner string, id string, tailLines int) (string, Info, error) {
	s.mu.Lock()
	mp, ok := s.procs[procKey{owner, id}]
	if !ok {
		defer s.mu.Unlock()
		return "", Info{}, s.unknownIDErrLocked(owner, id)
	}
	mp.lastRead = time.Now()
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
	return s.stopFor("", id)
}

func (s *Supervisor) stopFor(owner string, id string) (Info, error) {
	s.mu.Lock()
	mp, ok := s.procs[procKey{owner, id}]
	if !ok {
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
	return s.stopAsyncFor("", id)
}

func (s *Supervisor) stopAsyncFor(owner string, id string) (Info, error) {
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
	return s.removeFor("", id)
}

func (s *Supervisor) removeFor(owner string, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	mp, ok := s.procs[procKey{owner, id}]
	if !ok {
		return s.unknownIDErrLocked(owner, id)
	}
	if mp.info.State == StateRunning {
		return fmt.Errorf("process %s is running — stop it first", id)
	}
	delete(s.procs, procKey{owner, id})
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

// admitLocked says whether owner may start one more process. Its own limit
// is told with its own count, which it can act on. The terminal answers to
// nothing else. A named owner also shares the host ceilings, and those are
// told without a number or a name.
func (s *Supervisor) admitLocked(owner string) error {
	if s.closed {
		return fmt.Errorf("supervisor closed")
	}
	running, _ := s.countLocked(func(mp *managedProcess) bool { return mp.owner == owner })
	if running >= maxRunning {
		return fmt.Errorf("%d processes already running (limit %d) — stop one first (@proc stop)", running, maxRunning)
	}
	if owner == "" {
		return nil
	}
	namedRunning, namedTracked := s.countLocked(named)
	if namedRunning >= maxRunningHost {
		return errHostAtCapacity
	}
	if namedTracked >= maxRetainedHost && s.evictableForLocked(owner) == (procKey{}) {
		return errHostAtCapacity
	}
	return nil
}

// named selects the processes of the sessions, as opposed to the terminal's.
func named(mp *managedProcess) bool { return mp.owner != "" }

// countLocked counts the running and the tracked processes match selects.
func (s *Supervisor) countLocked(match func(*managedProcess) bool) (running, tracked int) {
	for _, mp := range s.procs {
		if !match(mp) {
			continue
		}
		tracked++
		if mp.info.State == StateRunning {
			running++
		}
	}
	return running, tracked
}

// evictForLocked makes room for one more entry of owner. Only EXITED
// entries ever go; a running process is never evicted. Under its own limit
// it is owner's oldest, so one session's activity never costs another its
// logs — nor the terminal's, nor the terminal theirs. At the ceiling the
// named owners share, it is still owner's own first, then the entry of any
// session that has gone unused the longest (see evictableForLocked).
func (s *Supervisor) evictForLocked(owner string) {
	for {
		_, mine := s.countLocked(func(mp *managedProcess) bool { return mp.owner == owner })
		_, namedTracked := s.countLocked(named)
		atCeiling := owner != "" && namedTracked >= maxRetainedHost
		if mine < maxRetained && !atCeiling {
			return
		}
		key := s.oldestExitedLocked(owner)
		if key == (procKey{}) && atCeiling {
			key = s.evictableForLocked(owner)
		}
		if key == (procKey{}) {
			return // everything is running; the running limits bound this
		}
		delete(s.procs, key)
	}
}

// oldestExitedLocked is owner's oldest exited entry, the zero key if none.
func (s *Supervisor) oldestExitedLocked(owner string) procKey {
	var oldestKey procKey
	var oldest time.Time
	for key, mp := range s.procs {
		if mp.owner != owner || mp.info.State != StateExited {
			continue
		}
		if oldestKey == (procKey{}) || mp.info.Ended.Before(oldest) {
			oldestKey, oldest = key, mp.info.Ended
		}
	}
	return oldestKey
}

// lastUsed is when an exited entry was last of use to its owner: the later
// of its exit and its last read.
func (mp *managedProcess) lastUsed() time.Time {
	if mp.lastRead.After(mp.info.Ended) {
		return mp.lastRead
	}
	return mp.info.Ended
}

// evictableForLocked is the entry a start by a named owner may evict at the
// tracked ceiling: its own oldest exited one, else the least recently used
// exited entry of any session, provided it has gone unused for at least
// unusedEntryAfter. Whether that session has something else running does
// not matter — it is the entry that was abandoned. The terminal's entries
// are never candidates.
func (s *Supervisor) evictableForLocked(owner string) procKey {
	if key := s.oldestExitedLocked(owner); key != (procKey{}) {
		return key
	}
	cutoff := time.Now().Add(-unusedEntryAfter)
	var lruKey procKey
	var lru time.Time
	for key, mp := range s.procs {
		if mp.owner == "" || mp.info.State != StateExited {
			continue
		}
		used := mp.lastUsed()
		if used.After(cutoff) {
			continue
		}
		if lruKey == (procKey{}) || used.Before(lru) {
			lruKey, lru = key, used
		}
	}
	return lruKey
}

// ownedBy reports whether a listing scoped to owner includes the process;
// a nil owner is the process-wide listing.
func (mp *managedProcess) ownedBy(owner *string) bool {
	return owner == nil || mp.owner == *owner
}

// unknownIDErrLocked is the answer for an id owner's table does not hold.
// It is the same whether the id was never issued or names a process of
// another owner — existence is not disclosed either — and it lists only
// owner's own ids.
func (s *Supervisor) unknownIDErrLocked(owner, id string) error {
	ids := make([]string, 0, len(s.procs))
	for key := range s.procs {
		if key.owner == owner {
			ids = append(ids, key.id)
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
