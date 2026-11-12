/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/diillson/chatcli/cli/plugins"
	"github.com/diillson/chatcli/cli/scheduler"
	"github.com/diillson/chatcli/cli/scheduler/builtins"
)

// newRemoteAdapter is the @scheduler adapter of a ChatCLI attached to a
// real scheduler daemon over its socket, as a terminal is when
// "chatcli daemon start" is running.
func newRemoteAdapter(t *testing.T) (*schedulerPluginAdapter, *scheduler.Scheduler) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the daemon listens on a UNIX socket")
	}
	cfg := scheduler.DefaultConfig()
	cfg.DataDir = t.TempDir()
	cfg.DefaultPollInterval = 20 * time.Millisecond
	cfg.AuditEnabled = false
	cfg.SnapshotInterval = 0
	cfg.WALGCInterval = 0
	cfg.DaemonAutoConnect = false
	s, err := scheduler.New(cfg, scheduler.NewNoopBridge(), scheduler.SchedulerDeps{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	builtins.RegisterAll(s)
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	// A UNIX socket path is short; t.TempDir's nested name can exceed it.
	dir, err := os.MkdirTemp("", "cs")
	if err != nil {
		t.Fatal(err)
	}
	d := scheduler.NewDaemon(s, filepath.Join(dir, "s.sock"), nil)
	if err := d.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	client, err := scheduler.Dial(filepath.Join(dir, "s.sock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = client.Close()
		_ = d.Stop(2 * time.Second)
		_ = os.RemoveAll(dir)
	})
	return &schedulerPluginAdapter{cli: &ChatCLI{schedulerRemote: client}}, s
}

func decodeToolOut(t *testing.T, raw string) scheduler.ToolOutput {
	t.Helper()
	var out scheduler.ToolOutput
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("decode %q: %v", raw, err)
	}
	return out
}

var remoteUser = plugins.SchedulerOwner{Kind: "user", ID: "tester"}

// Through the daemon, the tool reaches a job by the name it chose, as it
// does in-process.
func TestRemoteAdapterResolvesJobNames(t *testing.T) {
	a, s := newRemoteAdapter(t)
	ctx := context.Background()
	raw, err := a.ScheduleJob(ctx, remoteUser, `{"name":"nightly","when":"+1h","do":"noop"}`)
	if err != nil {
		t.Fatal(err)
	}
	id := decodeToolOut(t, raw).JobID
	if id == "" {
		t.Fatalf("no job id: %s", raw)
	}
	for _, ref := range []string{string(id), "nightly", string(id)[:6]} {
		raw, err = a.QueryJob(ctx, remoteUser, `{"id":"`+ref+`"}`)
		if err != nil {
			t.Fatalf("query %q: %v", ref, err)
		}
		if out := decodeToolOut(t, raw); out.Job == nil || out.Job.ID != id {
			t.Fatalf("query %q returned %s", ref, raw)
		}
	}
	if _, err = a.QueryJob(ctx, remoteUser, `{"id":"ghost"}`); err == nil {
		t.Fatal("an unknown name was resolved")
	}
	if _, err = a.QueryJob(ctx, remoteUser, `{}`); err == nil || !strings.Contains(err.Error(), "id is required") {
		t.Fatalf("a query with no id: %v", err)
	}
	if _, err = a.PauseJob(ctx, remoteUser, `{"id":"nightly"}`); err != nil {
		t.Fatal(err)
	}
	if j, _ := s.Query(id); j.Status != scheduler.StatusPaused {
		t.Fatalf("pause by name left the job %s", j.Status)
	}
	if _, err = a.ResumeJob(ctx, remoteUser, `{"id":"nightly"}`); err != nil {
		t.Fatal(err)
	}
	if _, err = a.CancelJob(ctx, remoteUser, `{"id":"nightly","reason":"done"}`); err != nil {
		t.Fatal(err)
	}
	if j, _ := s.Query(id); j.Status != scheduler.StatusCancelled {
		t.Fatalf("cancel by name left the job %s", j.Status)
	}
	raw, err = a.ListJobs(ctx, remoteUser, `{"filter":{"include_terminal":true}}`)
	if err != nil || len(decodeToolOut(t, raw).Jobs) != 1 {
		t.Fatalf("list: %s %v", raw, err)
	}

	// Clearing history goes through the daemon as well.
	raw, err = a.ClearJobs(ctx, remoteUser, `{"filter":{"statuses":["pending"]}}`)
	if err == nil {
		t.Fatalf("clearing a live status was accepted: %s", raw)
	}
	raw, err = a.ClearJobs(ctx, remoteUser, `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if out := decodeToolOut(t, raw); !out.OK || len(out.Removed) != 1 || out.Removed[0] != id {
		t.Fatalf("clear removed %v", out.Removed)
	}
	if _, err := s.Query(id); err == nil {
		t.Fatal("the cleared job is still in the daemon")
	}
}

// A synchronous wait through the daemon used to return as soon as the job
// was queued, which a model reads as "the condition holds".
func TestRemoteAdapterSynchronousWaitWaits(t *testing.T) {
	a, s := newRemoteAdapter(t)
	ready := filepath.Join(t.TempDir(), "ready")
	go func() {
		time.Sleep(300 * time.Millisecond)
		_ = os.WriteFile(ready, []byte("x"), 0o600)
	}()
	start := time.Now()
	raw, err := a.WaitUntil(context.Background(), remoteUser, `{"name":"gate","until":"file:`+ready+`","poll":"20ms"}`)
	if err != nil {
		t.Fatal(err)
	}
	out := decodeToolOut(t, raw)
	if took := time.Since(start); took < 250*time.Millisecond {
		t.Fatalf("the wait returned after %v, before the condition was met", took)
	}
	if !out.OK || out.Status != scheduler.StatusCompleted || out.Job == nil {
		t.Fatalf("wait result: %s", raw)
	}

	// async returns at once with the job still waiting.
	never := filepath.Join(t.TempDir(), "never")
	raw, err = a.WaitUntil(context.Background(), remoteUser, `{"name":"bg","until":"file:`+never+`","async":true}`)
	if err != nil {
		t.Fatal(err)
	}
	bg := decodeToolOut(t, raw)
	if !bg.OK || bg.JobID == "" {
		t.Fatalf("async wait: %s", raw)
	}
	if j, _ := s.Query(bg.JobID); j == nil || j.Status.IsTerminal() {
		t.Fatalf("the async wait is not live: %+v", j)
	}

	// max_block lets go of the caller and leaves the job running.
	start = time.Now()
	raw, err = a.WaitUntil(context.Background(), remoteUser, `{"name":"capped","until":"file:`+never+`","max_block":"600ms"}`)
	if err != nil {
		t.Fatal(err)
	}
	capped := decodeToolOut(t, raw)
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("max_block held the caller for %v", took)
	}
	if capped.OK || !strings.Contains(capped.Error, "KEEPS RUNNING") {
		t.Fatalf("capped wait: %s", raw)
	}
	if j, _ := s.Query(capped.JobID); j == nil || j.Status.IsTerminal() {
		t.Fatalf("the capped wait's job did not keep running: %+v", j)
	}

	// A cancelled caller is released too. The cancel is sent once the job
	// exists, so it lands while the adapter is holding and not during the
	// enqueue (on a slow machine a fixed delay did, and the test read that
	// as a failure).
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if _, rerr := s.ResolveRef("cancelled"); rerr == nil {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		cancel()
	}()
	released := make(chan struct{})
	go func() {
		defer close(released)
		raw, err = a.WaitUntil(ctx, remoteUser, `{"name":"cancelled","until":"file:`+never+`"}`)
	}()
	select {
	case <-released:
	case <-time.After(10 * time.Second):
		t.Fatal("a cancelled wait kept holding its caller")
	}
	// Released either with an error or with a reply that says so; never
	// with a success.
	if err == nil {
		if out := decodeToolOut(t, raw); out.OK || out.Error == "" {
			t.Fatalf("a cancelled wait reported success: %s", raw)
		}
	}
	if id, rerr := s.ResolveRef("cancelled"); rerr == nil {
		if j, _ := s.Query(id); j == nil || j.Status.IsTerminal() {
			t.Fatalf("cancelling the caller ended the job: %+v", j)
		}
	}
}

// With neither scheduler wired the adapter says so.
func TestAdapterWithoutSchedulerIsClosed(t *testing.T) {
	a := &schedulerPluginAdapter{cli: &ChatCLI{}}
	if _, err := a.ClearJobs(context.Background(), remoteUser, `{}`); err == nil {
		t.Fatal("no scheduler, yet clear succeeded")
	}
}
