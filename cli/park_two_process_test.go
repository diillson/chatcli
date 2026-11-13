//go:build !windows

/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * Two chatcli processes over one scheduler store and one park directory.
 *
 * The sweep decides whether a snapshot may be deleted from what it can
 * establish about OTHER processes. Three rounds of fixes each opened a new
 * hole there, and each time the tests had stubbed liveness and hand-written
 * the snapshot and job state, so the holes were in what the tests assumed.
 * Nothing is assumed here: terminal A is a real child process that really
 * parks, its scheduler really fires the job, its token really sits in its
 * resume queue; terminal B is this process, started before A parked, and
 * its sweep sees exactly what a second terminal would see. The child is
 * never stopped or signaled except to be killed, and exits by itself if
 * this process disappears.
 */
package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/diillson/chatcli/cli/agent/park"
	"github.com/diillson/chatcli/cli/plugins"
	"github.com/diillson/chatcli/cli/scheduler"
	"github.com/diillson/chatcli/cli/scheduler/builtins"
	"go.uber.org/zap"
)

const (
	parkOwnerDirEnv  = "CHATCLI_TEST_PARK_OWNER_DIR"
	parkOwnerModeEnv = "CHATCLI_TEST_PARK_OWNER_MODE"
)

// parkTerminal is a chatcli terminal as far as parks go: a real scheduler
// on the shared store with the real bridge and the real actions, and an
// agent whose resumed runs go through the real path with only the LLM loop
// replaced.
func parkTerminal(t *testing.T, storeDir string, loop func(context.Context) error) *ChatCLI {
	t.Helper()
	cli := &ChatCLI{logger: zap.NewNop()}
	cfg := scheduler.DefaultConfig()
	cfg.DataDir = storeDir
	cfg.AuditEnabled = false
	cfg.SnapshotInterval = 0
	cfg.WALGCInterval = 0
	cfg.DaemonAutoConnect = false
	bridge := newSchedulerBridge(cli)
	s, err := scheduler.New(cfg, bridge, scheduler.SchedulerDeps{}, zap.NewNop())
	if err != nil {
		t.Fatalf("scheduler.New: %v", err)
	}
	builtins.RegisterAll(s)
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("scheduler.Start: %v", err)
	}
	t.Cleanup(func() { s.DrainAndShutdown(2 * time.Second) })
	cli.scheduler, cli.schedulerBridge = s, bridge
	cli.agentMode = &AgentMode{cli: cli, logger: zap.NewNop(), resumeLoop: loop}
	return cli
}

func touch(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func fileExists(dir, name string) bool {
	_, err := os.Stat(filepath.Join(dir, name))
	return err == nil
}

func waitForFile(t *testing.T, dir, name string, timeout time.Duration) string {
	t.Helper()
	for deadline := time.Now().Add(timeout); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if data, err := os.ReadFile(filepath.Join(dir, name)); err == nil {
			return string(data)
		}
	}
	t.Fatalf("timed out waiting for %q", name)
	return ""
}

// TestParkOwnerProcess is terminal A. It is not a test of its own: the
// two-process tests run it as a child and it skips otherwise. It parks,
// stays mid-turn while its job fires and its resume queues, and drains
// when told to.
func TestParkOwnerProcess(t *testing.T) {
	dir := os.Getenv(parkOwnerDirEnv)
	if dir == "" {
		t.Skip("run as the child of the two-process park tests")
	}
	// The parent holds the other end of stdin for as long as it lives. If
	// it goes away without cleaning up — killed, crashed — the read ends
	// and so does this process: no child is ever left behind.
	go func() {
		_, _ = io.Copy(io.Discard, os.Stdin)
		os.Exit(3)
	}()
	cli := parkTerminal(t, filepath.Join(dir, "store"), func(context.Context) error {
		touch(t, dir, "resumed", "")
		return nil
	})
	// Mid-turn: a wake that arrives now is queued, to be drained when the
	// terminal is back at its prompt.
	cli.isExecuting.Store(true)
	req := park.Request{Mode: park.ModeDelay, Delay: 300 * time.Millisecond}
	if os.Getenv(parkOwnerModeEnv) == "for_cmd" {
		req = park.Request{Mode: park.ModeForCmd, Command: "true", Interval: 500 * time.Millisecond, Deadline: time.Now().Add(2 * time.Minute)}
	}
	long := os.Getenv(parkOwnerModeEnv) == "long"
	if long {
		req = park.Request{Mode: park.ModeDelay, Delay: time.Hour}
	}
	if err := cli.agentMode.handleAgentPark(context.Background(), req, "", ""); !errors.Is(err, errAgentParkedRequested) {
		t.Fatalf("park: %v", err)
	}
	active, ok := cli.latestActivePark()
	if !ok {
		t.Fatal("the park was not registered")
	}
	touch(t, dir, "token", active.Token)
	if long {
		// Parked for an hour: nothing fires; just stay alive until killed.
		waitForFile(t, dir, "drain", 3*time.Minute)
		return
	}
	for deadline := time.Now().Add(90 * time.Second); !cli.hasPendingResume(active.Token); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the park's job never fired")
		}
	}
	touch(t, dir, "queued", "")
	waitForFile(t, dir, "drain", 3*time.Minute)
	cli.isExecuting.Store(false)
	for cli.drainPendingResumes(context.Background()) {
	}
	touch(t, dir, "done", "")
}

// twoTerminals sets up the shared directories, starts terminal B in this
// process and then terminal A as a child.
func twoTerminals(t *testing.T, mode string, loop func(context.Context) error) (b *ChatCLI, a *exec.Cmd, dir string) {
	t.Helper()
	dir = t.TempDir()
	t.Setenv("CHATCLI_PARK_DIR", filepath.Join(dir, "parks"))
	t.Setenv("HOME", filepath.Join(dir, "home"))
	if err := os.MkdirAll(filepath.Join(dir, "home"), 0o700); err != nil {
		t.Fatal(err)
	}
	b = parkTerminal(t, filepath.Join(dir, "store"), loop) // B is running before A parks
	plugins.SetParkCanceller(newLiveParkAdapter(b))
	t.Cleanup(func() { plugins.SetParkCanceller(nil) })

	a = exec.Command(os.Args[0], "-test.run=^TestParkOwnerProcess$", "-test.count=1", "-test.timeout=5m")
	a.Env = append(os.Environ(), parkOwnerDirEnv+"="+dir, parkOwnerModeEnv+"="+mode)
	var out bytes.Buffer
	a.Stdout, a.Stderr = &out, &out
	lifeline, err := a.StdinPipe() // see the watchdog in TestParkOwnerProcess
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Start(); err != nil {
		t.Fatalf("start terminal A: %v", err)
	}
	t.Cleanup(func() {
		_ = lifeline.Close()
		_ = a.Process.Kill()
		_, _ = a.Process.Wait()
		if t.Failed() {
			t.Logf("terminal A said:\n%s", out.String())
		}
	})
	return b, a, dir
}

// waitParkJobsDone waits until the store both terminals share shows every
// job of the park as finished — as the owner's scheduler wrote it, a
// moment after the wake was delivered. This is the state in which the
// scheduler alone would call the park finished.
func waitParkJobsDone(t *testing.T, b *ChatCLI, token string) {
	t.Helper()
	for deadline := time.Now().Add(60 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		jobs, err := b.scheduler.StoredJobs(func(j *scheduler.Job) bool { return j.Owner.Kind == "park" && j.Owner.Tag == token })
		if err != nil {
			t.Fatal(err)
		}
		pending := len(jobs) == 0
		for _, j := range jobs {
			pending = pending || !j.Status.IsTerminal()
		}
		if !pending {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the park's jobs never finished in the store: %d job(s)", len(jobs))
		}
	}
}

// sweepBothWays runs the sweep as both of its callers do and fails if
// either removed token.
func sweepBothWays(t *testing.T, b *ChatCLI, token, when string) (tool string) {
	t.Helper()
	tool, err := plugins.NewBuiltinParkPlugin().Execute(context.Background(), []string{"prune"})
	if err != nil {
		t.Fatalf("%s: @park prune: %v", when, err)
	}
	cmd := captureStdout(t, b.parkPrune)
	if !parkOnDisk(token) {
		t.Fatalf("%s: the other terminal's prune deleted the park.\n@park prune: %s\n/parked prune: %s", when, tool, cmd)
	}
	return tool
}

// A parks; its job fires and completes at once (the action only notifies);
// A is mid-turn, so the token sits in its resume queue. B prunes. The park
// is A's and A is alive: B must leave it, and A's drain must then resume
// the agent.
func TestPruneFromAnotherTerminalKeepsAQueuedResume(t *testing.T) {
	b, _, dir := twoTerminals(t, "delay", nil)
	token := waitForFile(t, dir, "token", 60*time.Second)
	waitForFile(t, dir, "queued", 90*time.Second)
	waitParkJobsDone(t, b, token)

	tool := sweepBothWays(t, b, token, "with the resume queued in the owner")
	if !strings.Contains(tool, parkShort(token)) || !strings.Contains(tool, "still waiting") {
		t.Errorf("the tool must report the park as kept, still waiting: %s", tool)
	}
	// A note for it goes through its owner, not into its snapshot from here.
	if out := captureStdout(t, func() { b.handleParkNoteCommand("--token " + token + " hello") }); !strings.Contains(out, parkShort(token)) || parkDirectives(t, token) != "" {
		t.Errorf("a note for a park another live process owns must be refused: %q", out)
	}

	touch(t, dir, "drain", "")
	waitForFile(t, dir, "done", 60*time.Second)
	if !fileExists(dir, "resumed") {
		t.Fatal("the owner's drain did not resume the agent")
	}
	if parkOnDisk(token) {
		t.Fatal("a resume that completed retires its snapshot")
	}
}

// The owner dies after its job fired and before it drained. The job is
// terminal and the owner is gone, and still the agent never continued:
// the park is kept for retry, reported with the way to retry — and
// /resume from the surviving terminal does resume it.
func TestOwnerDeadAfterTheJobFiredIsKeptForRetry(t *testing.T) {
	resumed := false
	b, a, dir := twoTerminals(t, "delay", func(context.Context) error {
		resumed = true
		return nil
	})
	token := waitForFile(t, dir, "token", 60*time.Second)
	waitForFile(t, dir, "queued", 90*time.Second)
	waitParkJobsDone(t, b, token)
	if err := a.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_, _ = a.Process.Wait()

	tool := sweepBothWays(t, b, token, "after the owner died with the resume queued")
	if !strings.Contains(tool, "/resume "+parkShort(token)) {
		t.Errorf("the tool must report it as kept for retry, with the way to retry: %s", tool)
	}
	if rep := b.sweepDeadParks(0); len(rep.retry) != 1 || rep.retry[0] != token {
		t.Fatalf("kept for retry: %+v", rep)
	}
	_ = captureStdout(t, func() { b.handleResumeCommand(context.Background(), "/resume "+token) })
	if !resumed || parkOnDisk(token) {
		t.Fatalf("/resume from the surviving terminal must continue the agent (resumed=%v, snapshot on disk=%v)", resumed, parkOnDisk(token))
	}
}

// A polling park: its snapshot names the POLL job, while the wake is a
// separate resume job. Right after the wake is delivered both records
// still say running; a moment later both are finished — and through all
// of it the agent is waiting for its owner's drain. B prunes at both
// points and leaves the park; A drains and the agent resumes.
//
// (The instant in between — poll job finished, resume job still pending —
// is not held open here: in a real run the resume fires before either
// record is rewritten. The rule does not depend on it: with a live owner
// no job state is consulted.)
func TestPruneFromAnotherTerminalKeepsAPollingParkAroundItsWake(t *testing.T) {
	b, _, dir := twoTerminals(t, "for_cmd", nil)
	token := waitForFile(t, dir, "token", 60*time.Second)
	waitForFile(t, dir, "queued", 90*time.Second)
	sweepBothWays(t, b, token, "with the wake just delivered")
	waitParkJobsDone(t, b, token)
	sweepBothWays(t, b, token, "with both of the park's jobs finished and the resume queued in the owner")

	touch(t, dir, "drain", "")
	waitForFile(t, dir, "done", 60*time.Second)
	if !fileExists(dir, "resumed") {
		t.Fatal("the owner's drain did not resume the agent")
	}
}
