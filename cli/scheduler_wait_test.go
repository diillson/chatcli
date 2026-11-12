/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/diillson/chatcli/cli/scheduler"
	"github.com/diillson/chatcli/cli/scheduler/builtins"
	"github.com/diillson/chatcli/i18n"
)

// newWaitCLI is a ChatCLI over a real scheduler with the builtin
// conditions, plus the path of a file whose creation satisfies the
// condition "file:<path>".
func newWaitCLI(t *testing.T) (*ChatCLI, string) {
	t.Helper()
	c := newCLIWithScheduler(t)
	builtins.RegisterAll(c.scheduler)
	prevPoll, prevBeat := followPollInterval, followBeatInterval
	followPollInterval, followBeatInterval = 10*time.Millisecond, time.Hour
	t.Cleanup(func() { followPollInterval, followBeatInterval = prevPoll, prevBeat })
	return c, filepath.Join(t.TempDir(), "ready")
}

func onlyJob(t *testing.T, c *ChatCLI) *scheduler.Job {
	t.Helper()
	list := c.scheduler.List(scheduler.ListFilter{IncludeTerminal: true})
	if len(list) != 1 {
		t.Fatalf("want exactly one job, have %d", len(list))
	}
	j, err := c.scheduler.Query(list[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	return j
}

func awaitJobStatus(t *testing.T, c *ChatCLI, id scheduler.JobID, want scheduler.JobStatus) *scheduler.Job {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if j, err := c.scheduler.Query(id); err == nil && j.Status == want {
			return j
		}
		time.Sleep(10 * time.Millisecond)
	}
	j, _ := c.scheduler.Query(id)
	t.Fatalf("job %s never reached %s: %+v", id, want, j)
	return nil
}

// The reported bug: /wait held the prompt until the condition was met (up
// to 45 minutes). It must register the job and return.
func TestWaitRegistersInBackgroundAndReturns(t *testing.T) {
	c, ready := newWaitCLI(t)
	start := time.Now()
	out := captureStdout(t, func() {
		c.handleWaitCommand(context.Background(), "/wait --until file:"+ready+" --every 20ms --timeout 1m")
	})
	if took := time.Since(start); took > 3*time.Second {
		t.Fatalf("/wait held the prompt for %v with the condition unmet", took)
	}
	j := onlyJob(t, c)
	if !strings.Contains(out, string(j.ID)) || !strings.Contains(out, "/jobs cancel "+string(j.ID)) {
		t.Fatalf("output does not say where the job is:\n%s", out)
	}
	if j.Status.IsTerminal() || j.Tags[notifyTag] != "on" || !c.jobNotices.owns(j.ID) {
		t.Fatalf("job after /wait: status=%s tags=%v", j.Status, j.Tags)
	}
	// The scheduler keeps polling with nobody watching.
	if err := os.WriteFile(ready, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	awaitJobStatus(t, c, j.ID, scheduler.StatusCompleted)
}

func TestWaitBlockHoldsUntilTheConditionIsMet(t *testing.T) {
	c, ready := newWaitCLI(t)
	go func() {
		time.Sleep(200 * time.Millisecond)
		_ = os.WriteFile(ready, []byte("x"), 0o600)
	}()
	start := time.Now()
	out := captureStdout(t, func() {
		c.handleWaitCommand(context.Background(), "/wait file:"+ready+" --every 20ms --block --name gate")
	})
	if took := time.Since(start); took < 150*time.Millisecond {
		t.Fatalf("--block returned after %v, before the condition was met", took)
	}
	j := onlyJob(t, c)
	if j.Status != scheduler.StatusCompleted {
		t.Fatalf("job is %s", j.Status)
	}
	want := i18n.T("scheduler.notify.wait_met", "gate", j.ID, shortDuration(jobElapsed(j)))
	if !strings.Contains(out, want) {
		t.Fatalf("output lacks %q:\n%s", want, out)
	}
	if c.CanInterrupt() {
		t.Fatal("the follow left itself registered as interruptible")
	}
}

// Ctrl+C during a blocking wait reaches the process-wide handler, which
// cancels the running operation when one is registered and shuts the
// application down otherwise. The follow must be that operation, and
// letting go of it must leave the job running.
func TestCtrlCDuringBlockReleasesThePromptAndKeepsTheJob(t *testing.T) {
	c, ready := newWaitCLI(t)
	done := make(chan string, 1)
	// captureStdout is not safe to run concurrently with this test's own
	// prints, so the follow runs under it in one goroutine and the test
	// only signals.
	go func() {
		done <- captureStdout(t, func() {
			c.handleWaitCommand(context.Background(), "/wait file:"+ready+" --every 20ms --block")
		})
	}()
	deadline := time.Now().Add(3 * time.Second)
	for !c.CanInterrupt() {
		if time.Now().After(deadline) {
			t.Fatal("the blocking wait never registered as a cancellable operation; Ctrl+C would end the session")
		}
		time.Sleep(5 * time.Millisecond)
	}
	c.CancelOperation() // what the SIGINT handler does while CanInterrupt
	var out string
	select {
	case out = <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Ctrl+C did not release the prompt")
	}
	j := onlyJob(t, c)
	if j.Status.IsTerminal() {
		t.Fatalf("letting go of the prompt ended the job (%s)", j.Status)
	}
	if !strings.Contains(out, i18n.T("scheduler.follow.detached", j.ID, j.ID)) {
		t.Fatalf("output does not say the job keeps running:\n%s", out)
	}
	if c.jobNotices.isFollowed(j.ID) {
		t.Fatal("the job is still marked as followed, so its end would not be announced")
	}
}

// On a headless surface a held command pins the capture slot every
// session shares: --block is declined and the job runs in the background.
func TestWaitBlockIsDeclinedHeadless(t *testing.T) {
	c, ready := newWaitCLI(t)
	c.unattended = true
	start := time.Now()
	out := captureStdout(t, func() {
		c.handleWaitCommand(context.Background(), "/wait file:"+ready+" --block")
	})
	if took := time.Since(start); took > 3*time.Second {
		t.Fatalf("--block held a headless surface for %v", took)
	}
	if !strings.Contains(out, i18n.T("scheduler.wait.block_unavailable")) {
		t.Fatalf("output does not explain why it did not hold:\n%s", out)
	}
	if j := onlyJob(t, c); j.Status.IsTerminal() {
		t.Fatalf("job is %s", j.Status)
	}
	// /jobs wait reports the job instead of holding.
	j := onlyJob(t, c)
	out = captureStdout(t, func() { c.jobsWait(context.Background(), []string{j.Name}) })
	if !strings.Contains(out, string(j.ID)) || !strings.Contains(out, i18n.T("scheduler.wait.block_unavailable")) {
		t.Fatalf("/jobs wait headless:\n%s", out)
	}
}

func TestWaitRejectsWhatItCannotRun(t *testing.T) {
	c, _ := newWaitCLI(t)
	for _, line := range []string{
		"/wait --until nonsense-condition",
		"/wait tcp://db:5432 --timeout 10x",
		"/wait tcp://db:5432 --every",
		"/wait tcp://db:5432 --max-polls many",
		"/wait tcp://db:5432 --on-timeout explode",
		"/wait tcp://db:5432 --frobnicate",
		"/wait tcp://db:5432 stray",
	} {
		out := captureStdout(t, func() { c.handleWaitCommand(context.Background(), line) })
		if !strings.Contains(out, "❌") {
			t.Errorf("%s was accepted:\n%s", line, out)
		}
	}
	if n := len(c.scheduler.List(scheduler.ListFilter{IncludeTerminal: true})); n != 0 {
		t.Fatalf("%d jobs were created from rejected commands", n)
	}
}

func TestParseWaitArgs(t *testing.T) {
	in, err := parseWaitArgs([]string{"tcp://db:5432"})
	if err != nil || in.Until != "tcp://db:5432" || !in.Async || in.Do != "noop" || in.When != "when-ready" || in.Tags[notifyTag] != "on" {
		t.Fatalf("bare condition: %+v %v", in, err)
	}
	// --async is what /wait now does by default; it stays accepted.
	if in, err = parseWaitArgs([]string{"--until", "tcp://db:5432", "--async"}); err != nil || !in.Async {
		t.Fatalf("--async: %+v %v", in, err)
	}
	for _, flag := range []string{"--block", "--sync", "--foreground", "--fg"} {
		if in, err = parseWaitArgs([]string{"tcp://db:5432", flag}); err != nil || in.Async {
			t.Fatalf("%s must hold the prompt: %+v %v", flag, in, err)
		}
	}
	in, err = parseWaitArgs([]string{
		"--until", "tcp://db:5432", "--then", "shell: make migrate", "--every", "5s", "--timeout", "10 min",
		"--action-timeout", "2m", "--max-polls", "30", "--max-retries", "2", "--on-timeout", "fire_anyway",
		"--name", "db", "--description", "wait for the db", "--tag", "env=dev", "--ttl", "1h",
		"--depends-on", "abc", "--skill", "ops", "--i-know", "--quiet",
	})
	if err != nil {
		t.Fatal(err)
	}
	if in.Do != "shell: make migrate" || in.PollInterval != "5s" || in.WaitTimeout != "10 min" || in.Timeout != "2m" ||
		in.MaxPolls != 30 || in.MaxRetries != 2 || in.OnTimeout != "fire_anyway" || in.Name != "db" ||
		in.Description != "wait for the db" || in.Tags["env"] != "dev" || in.TTL != "1h" ||
		len(in.DependsOn) != 1 || len(in.Skills) != 1 || !in.IKnow || in.Tags[notifyTag] != "off" {
		t.Fatalf("flags not carried: %+v", in)
	}
	// An explicit notify tag wins over the default.
	if in, _ = parseWaitArgs([]string{"tcp://db:5432", "--tag", "notify=off"}); in.Tags[notifyTag] != "off" {
		t.Fatalf("explicit tag overridden: %v", in.Tags)
	}
	if _, err = parseWaitArgs([]string{"--then", "noop"}); err == nil {
		t.Fatal("a wait with no condition was accepted")
	}
}

func TestScheduleNotifyAndStrictNumbers(t *testing.T) {
	in, err := parseScheduleArgs([]string{"nightly", "--when", "+1h", "--do", "noop", "--notify"})
	if err != nil || in.Tags[notifyTag] != "on" {
		t.Fatalf("--notify: %+v %v", in, err)
	}
	if _, err = parseScheduleArgs([]string{"x", "--when", "+1h", "--do", "noop", "--max-retries", "lots"}); err == nil {
		t.Fatal("--max-retries lots was accepted")
	}
}

func TestJobEventEndsRun(t *testing.T) {
	cases := []struct {
		typ    scheduler.EventType
		status scheduler.JobStatus
		want   bool
	}{
		{scheduler.EventJobCompleted, scheduler.StatusCompleted, true},
		{scheduler.EventJobCompleted, scheduler.StatusPending, true}, // a recurring job's cycle
		{scheduler.EventJobFailed, scheduler.StatusFailed, true},
		{scheduler.EventJobTimedOut, scheduler.StatusWaiting, false}, // budget ran out, job not yet marked
		{scheduler.EventJobTimedOut, scheduler.StatusTimedOut, true},
		{scheduler.EventJobWaitTick, scheduler.StatusWaiting, false},
		{scheduler.EventJobCancelled, scheduler.StatusCancelled, false}, // the user did it
	}
	for _, c := range cases {
		if got := jobEventEndsRun(scheduler.Event{Type: c.typ, Status: c.status}); got != c.want {
			t.Errorf("%s/%s = %v, want %v", c.typ, c.status, got, c.want)
		}
	}
}

func TestFinishedWaitIsAnnouncedOnce(t *testing.T) {
	c, ready := newWaitCLI(t)
	_ = captureStdout(t, func() {
		c.handleWaitCommand(context.Background(), "/wait file:"+ready+" --every 20ms --name gate --then \"shell: echo migrated\" --i-know")
	})
	j := onlyJob(t, c)
	if err := os.WriteFile(ready, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The shell action has no bridge to run on here and fails; the notice
	// reports that end just the same.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if j, _ = c.scheduler.Query(j.ID); j.Status.IsTerminal() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("job never ended: %s", j.Status)
		}
		time.Sleep(10 * time.Millisecond)
	}
	evt := scheduler.Event{Type: scheduler.EventJobFailed, JobID: j.ID, Status: j.Status}
	if j.Status == scheduler.StatusCompleted {
		evt.Type = scheduler.EventJobCompleted
	}
	out := captureStdout(t, func() { c.announceJobEvent(context.Background(), evt) })
	if !strings.Contains(out, "gate") || !strings.Contains(out, i18n.T("scheduler.notify.hint", j.ID)) {
		t.Fatalf("the end of the wait was not announced:\n%s", out)
	}
	if again := captureStdout(t, func() { c.announceJobEvent(context.Background(), evt) }); strings.TrimSpace(again) != "" {
		t.Fatalf("announced twice:\n%s", again)
	}
}

func TestNoticeIsSkippedWhenNotWanted(t *testing.T) {
	c, ready := newWaitCLI(t)
	_ = captureStdout(t, func() {
		c.handleWaitCommand(context.Background(), "/wait file:"+ready+" --every 20ms --quiet --name silent")
		c.handleWaitCommand(context.Background(), "/wait file:"+ready+" --every 20ms --name held")
	})
	if err := os.WriteFile(ready, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	silent, _ := c.resolveJobRef(context.Background(), "silent")
	held, _ := c.resolveJobRef(context.Background(), "held")
	awaitJobStatus(t, c, silent, scheduler.StatusCompleted)
	awaitJobStatus(t, c, held, scheduler.StatusCompleted)

	// --quiet: no notice.
	out := captureStdout(t, func() {
		c.announceJobEvent(context.Background(), scheduler.Event{Type: scheduler.EventJobCompleted, JobID: silent, Status: scheduler.StatusCompleted})
	})
	if strings.TrimSpace(out) != "" {
		t.Fatalf("--quiet wait was announced:\n%s", out)
	}
	// A foreground follow reports the job itself.
	c.jobNotices.follow(held)
	out = captureStdout(t, func() {
		c.announceJobEvent(context.Background(), scheduler.Event{Type: scheduler.EventJobCompleted, JobID: held, Status: scheduler.StatusCompleted})
	})
	if strings.TrimSpace(out) != "" {
		t.Fatalf("a followed job was also announced:\n%s", out)
	}
	c.jobNotices.unfollow(held)
	// Outside the interactive loop nothing is printed at all: a one-shot
	// run's stdout is its answer, an RPC server's is the protocol.
	c.startJobNotices(context.Background())
	c.onSchedulerEvent(scheduler.Event{Type: scheduler.EventJobCompleted, JobID: held, Status: scheduler.StatusCompleted})
	time.Sleep(100 * time.Millisecond)
	if !c.jobNotices.firstTime(string(held) + "|" + string(scheduler.StatusCompleted)) {
		t.Fatal("a notice was issued outside the interactive loop")
	}
}

func TestJobsCommandsAcceptNames(t *testing.T) {
	c, ready := newWaitCLI(t)
	_ = captureStdout(t, func() {
		c.handleWaitCommand(context.Background(), "/wait file:"+ready+" --name api-up --timeout 5m")
	})
	j := onlyJob(t, c)
	awaitJobStatus(t, c, j.ID, scheduler.StatusWaiting)
	out := captureStdout(t, func() { c.handleJobsCommand(context.Background(), "/jobs show api-up") })
	if !strings.Contains(out, string(j.ID)) || !strings.Contains(out, "file_exists ") {
		t.Fatalf("/jobs show by name:\n%s", out)
	}
	out = captureStdout(t, func() { c.handleJobsCommand(context.Background(), "/jobs show "+string(j.ID)[:6]) })
	if !strings.Contains(out, string(j.ID)) {
		t.Fatalf("/jobs show by id prefix:\n%s", out)
	}
	out = captureStdout(t, func() { c.handleJobsCommand(context.Background(), "/jobs show ghost") })
	if !strings.Contains(out, i18n.T("scheduler.jobs.not_found", "ghost")) {
		t.Fatalf("/jobs show of an unknown name:\n%s", out)
	}
	_ = captureStdout(t, func() { c.handleJobsCommand(context.Background(), "/jobs cancel api-up") })
	awaitJobStatus(t, c, j.ID, scheduler.StatusCancelled)
}

func TestSchedulerCommandsRunHeadless(t *testing.T) {
	for _, cmd := range []string{"/schedule", "/wait", "/jobs"} {
		if !acpCommandAllow[cmd] {
			t.Errorf("%s is not runnable over ACP", cmd)
		}
	}
	for _, sub := range []string{"cancel", "pause", "resume", "show", "list", "logs", "wait", ""} {
		if webJobsTerminalOnly[sub] {
			t.Errorf("/jobs %s is kept from the web page", sub)
		}
	}
	for _, sub := range []string{"clear", "prune", "gc", "daemon"} {
		if !webJobsTerminalOnly[sub] {
			t.Errorf("/jobs %s must stay in the terminal", sub)
		}
	}
}

// Inside the interactive loop an event that ends a run reaches the notice
// loop and is announced.
func TestSchedulerEventIsAnnouncedInTheInteractiveLoop(t *testing.T) {
	c, ready := newWaitCLI(t)
	_ = captureStdout(t, func() {
		c.handleWaitCommand(context.Background(), "/wait file:"+ready+" --every 20ms --name looped")
	})
	if err := os.WriteFile(ready, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	id, _ := c.resolveJobRef(context.Background(), "looped")
	awaitJobStatus(t, c, id, scheduler.StatusCompleted)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.replInteractive.Store(true)
	out := captureStdout(t, func() {
		c.startJobNotices(ctx)
		c.onSchedulerEvent(scheduler.Event{Type: scheduler.EventJobWaitTick, JobID: id, Status: scheduler.StatusWaiting})
		c.onSchedulerEvent(scheduler.Event{Type: scheduler.EventJobCompleted, JobID: id, Status: scheduler.StatusCompleted})
		deadline := time.Now().Add(3 * time.Second)
		for c.jobNotices.firstTimePeek(string(id)+"|"+string(scheduler.StatusCompleted)) && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		time.Sleep(50 * time.Millisecond)
	})
	if !strings.Contains(out, "looped") {
		t.Fatalf("the finished wait was not announced:\n%s", out)
	}
}
