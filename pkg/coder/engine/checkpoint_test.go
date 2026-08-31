/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * checkpoint_test.go
 *
 * Shadow-git checkpoints: an automatic snapshot before a mutation, a listing,
 * and a restore that rewinds tracked files — all against a temp workspace and
 * a temp HOME so the real ChatCLI home is never touched. Skips cleanly where
 * git is absent. The scale guardrails (root guard, deadline, backoff/breaker,
 * artifact sweep) are covered by dedicated tests below.
 */
package engine

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func checkpointTestEngine(t *testing.T) (*Engine, *bytes.Buffer, string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv(checkpointEnv, "on")
	resetCheckpointTracker(t)

	work := t.TempDir()
	var out bytes.Buffer
	return NewEngine(&out, &out, work), &out, work
}

func writeWorkspaceFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// resetCheckpointTracker swaps in a fresh process-global tracker so tests
// never see throttle/breaker state from a previous test, restoring the
// original at cleanup.
func resetCheckpointTracker(t *testing.T) {
	t.Helper()
	prev := checkpoints
	checkpoints = newCheckpointTracker()
	t.Cleanup(func() { checkpoints = prev })
}

func TestAutoCheckpointAndRestore(t *testing.T) {
	e, out, work := checkpointTestEngine(t)
	ctx := context.Background()

	writeWorkspaceFile(t, work, "app.go", "package main // v1\n")
	if err := snapshotWorkspace(ctx, work, "seed"); err != nil {
		t.Fatalf("seed snapshot: %v", err)
	}

	resetCheckpointTracker(t)
	target := filepath.Join(work, "app.go")
	if err := e.Execute(ctx, "write", []string{"--file", target, "--content", "package main // v2\n"}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if data, _ := os.ReadFile(target); !strings.Contains(string(data), "v2") {
		t.Fatal("write did not land")
	}

	out.Reset()
	if err := e.Execute(ctx, "checkpoint", []string{"--list"}); err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out.String(), "checkpoint before seed") {
		t.Fatalf("listing missing seed checkpoint:\n%s", out.String())
	}

	gitDir, _ := shadowGitDir(work)
	hash, err := shadowGit(ctx, gitDir, work, "rev-list", "--max-count=1", "--reverse", "HEAD")
	if err != nil {
		t.Fatalf("rev-list: %v", err)
	}
	hash = strings.Fields(hash)[0]
	out.Reset()
	if err := e.Execute(ctx, "checkpoint", []string{"--restore", hash}); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if data, _ := os.ReadFile(target); !strings.Contains(string(data), "v1") {
		t.Fatalf("restore did not rewind the file: %s", data)
	}
}

func TestCheckpointDisabledAndEmpty(t *testing.T) {
	e, out, work := checkpointTestEngine(t)

	if err := e.Execute(context.Background(), "checkpoint", []string{"--list"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "No checkpoints") {
		t.Fatalf("empty listing wrong:\n%s", out.String())
	}

	t.Setenv(checkpointEnv, "off")
	resetCheckpointTracker(t)
	writeWorkspaceFile(t, work, "x.go", "package x\n")
	e.autoCheckpoint(context.Background(), "write")
	gitDir, _ := shadowGitDir(work)
	if _, err := os.Stat(filepath.Join(gitDir, "HEAD")); err == nil {
		t.Fatal("kill switch must prevent shadow repo creation")
	}
}

func TestCheckpointNeverTouchesUserGit(t *testing.T) {
	e, _, work := checkpointTestEngine(t)
	userGit := filepath.Join(work, ".git")
	if err := os.MkdirAll(userGit, 0o755); err != nil {
		t.Fatal(err)
	}
	writeWorkspaceFile(t, work, ".git/CANARY", "do-not-touch\n")

	resetCheckpointTracker(t)
	e.autoCheckpoint(context.Background(), "write")

	data, err := os.ReadFile(filepath.Join(userGit, "CANARY"))
	if err != nil || !strings.Contains(string(data), "do-not-touch") {
		t.Fatalf("user .git was disturbed: %v", err)
	}
	gitDir, _ := shadowGitDir(work)
	if !strings.Contains(gitDir, ".chatcli") {
		t.Fatalf("shadow git dir not under chatcli home: %s", gitDir)
	}
}

func TestUnsafeCheckpointRoot(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	fsRoot := string(os.PathSeparator)
	if vol := filepath.VolumeName(home); vol != "" {
		fsRoot = vol + string(os.PathSeparator)
	}

	cases := []struct {
		name   string
		root   string
		unsafe bool
	}{
		{"home itself", home, true},
		{"parent of home", filepath.Dir(home), true},
		{"filesystem root", fsRoot, true},
		{"project inside home", filepath.Join(home, "projects", "app"), false},
		{"unrelated temp dir", t.TempDir(), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, reason := unsafeCheckpointRoot(tc.root)
			if got != tc.unsafe {
				t.Fatalf("unsafeCheckpointRoot(%q) = %v (%s), want %v", tc.root, got, reason, tc.unsafe)
			}
			if got && reason == "" {
				t.Fatalf("unsafe root %q must carry a reason", tc.root)
			}
		})
	}
}

func TestAutoCheckpointRefusesBroadWorkspace(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv(checkpointEnv, "on")
	resetCheckpointTracker(t)

	var out bytes.Buffer
	e := NewEngine(&out, &out, home) // workspace == $HOME: the field failure

	e.autoCheckpoint(context.Background(), "exec")
	e.autoCheckpoint(context.Background(), "exec")

	gitDir, _ := shadowGitDir(home)
	if _, err := os.Stat(gitDir); err == nil {
		t.Fatal("no shadow repo may be created for an unsafe root")
	}
	if got := strings.Count(out.String(), "[checkpoint] automatic snapshots disabled"); got != 1 {
		t.Fatalf("want exactly one warning, got %d:\n%s", got, out.String())
	}
}

func TestManualCheckpointRefusesBroadWorkspace(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv(checkpointEnv, "on")
	resetCheckpointTracker(t)

	var out bytes.Buffer
	e := NewEngine(&out, &out, home)
	err := e.Execute(context.Background(), "checkpoint", []string{"--create"})
	if err == nil || !strings.Contains(err.Error(), "refusing to snapshot") {
		t.Fatalf("manual create on home must refuse with a clear error, got: %v", err)
	}

	if err := CheckpointWorkspace(home, "taskgraph"); err != nil {
		t.Fatalf("exported best-effort surface must skip, not fail: %v", err)
	}
	gitDir, _ := shadowGitDir(home)
	if _, statErr := os.Stat(gitDir); statErr == nil {
		t.Fatal("CheckpointWorkspace on unsafe root must be a no-op")
	}
}

func TestCheckpointTrackerBackoffAndBreaker(t *testing.T) {
	tr := newCheckpointTracker()
	now := time.Unix(1_000_000, 0)
	tr.now = func() time.Time { return now }
	root := "/some/project"
	fail := os.ErrDeadlineExceeded

	if !tr.begin(root) {
		t.Fatal("first attempt must be allowed")
	}
	if tr.begin(root) {
		t.Fatal("in-flight attempt must block a concurrent one")
	}
	if tripped := tr.finish(root, fail); tripped {
		t.Fatal("first failure must not trip the breaker")
	}

	if tr.begin(root) {
		t.Fatal("failure backoff must hold before it elapses")
	}
	now = now.Add(checkpointBackoff(1) + time.Second)
	if !tr.begin(root) {
		t.Fatal("attempt must be allowed after the first backoff")
	}
	if tripped := tr.finish(root, fail); tripped {
		t.Fatal("second failure must not trip the breaker")
	}

	now = now.Add(checkpointBackoff(2) + time.Second)
	if !tr.begin(root) {
		t.Fatal("attempt must be allowed after the second backoff")
	}
	if tripped := tr.finish(root, fail); !tripped {
		t.Fatalf("failure #%d must trip the session breaker", checkpointBreakerThreshold)
	}

	now = now.Add(24 * time.Hour)
	if tr.begin(root) {
		t.Fatal("a tripped breaker must hold for the rest of the session")
	}

	// A user interrupt is neutral: it must not walk toward the breaker.
	canceled := "/interrupted/project"
	for i := 0; i < checkpointBreakerThreshold+2; i++ {
		if !tr.begin(canceled) {
			t.Fatalf("interrupt #%d must not be throttled by failure backoff", i)
		}
		if tripped := tr.finish(canceled, fmt.Errorf("wrap: %w", context.Canceled)); tripped {
			t.Fatal("user interrupts must never trip the breaker")
		}
		now = now.Add(checkpointMinInterval + time.Second)
	}

	// Success resets the failure streak on a healthy root.
	other := "/other/project"
	if !tr.begin(other) {
		t.Fatal("healthy root must be allowed")
	}
	if tripped := tr.finish(other, nil); tripped {
		t.Fatal("success must never trip the breaker")
	}
	if tr.begin(other) {
		t.Fatal("throttle must hold right after a success")
	}
	now = now.Add(checkpointMinInterval + time.Second)
	if !tr.begin(other) {
		t.Fatal("throttle must reopen after the base interval")
	}
}

func TestCheckpointBackoffCap(t *testing.T) {
	if got := checkpointBackoff(0); got != checkpointMinInterval {
		t.Fatalf("backoff(0) = %v, want %v", got, checkpointMinInterval)
	}
	if got := checkpointBackoff(64); got != checkpointMaxBackoff {
		t.Fatalf("backoff must cap at %v, got %v", checkpointMaxBackoff, got)
	}
}

func TestSweepStaleArtifacts(t *testing.T) {
	gitDir := t.TempDir()
	packDir := filepath.Join(gitDir, "objects", "pack")
	if err := os.MkdirAll(packDir, 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	old := now.Add(-2 * time.Hour)

	staleLock := filepath.Join(gitDir, "index.lock")
	writeWorkspaceFile(t, gitDir, "index.lock", "")
	if err := os.Chtimes(staleLock, old, old); err != nil {
		t.Fatal(err)
	}
	writeWorkspaceFile(t, packDir, "tmp_pack_dead", "x")
	if err := os.Chtimes(filepath.Join(packDir, "tmp_pack_dead"), old, old); err != nil {
		t.Fatal(err)
	}
	writeWorkspaceFile(t, packDir, "tmp_pack_live", "x")
	writeWorkspaceFile(t, packDir, "pack-real.pack", "x")
	if err := os.Chtimes(filepath.Join(packDir, "pack-real.pack"), old, old); err != nil {
		t.Fatal(err)
	}

	sweepStaleArtifacts(gitDir, now)

	if _, err := os.Stat(staleLock); err == nil {
		t.Fatal("stale index.lock must be swept")
	}
	if _, err := os.Stat(filepath.Join(packDir, "tmp_pack_dead")); err == nil {
		t.Fatal("stale tmp pack must be swept")
	}
	if _, err := os.Stat(filepath.Join(packDir, "tmp_pack_live")); err != nil {
		t.Fatal("fresh tmp pack must survive (may belong to a live process)")
	}
	if _, err := os.Stat(filepath.Join(packDir, "pack-real.pack")); err != nil {
		t.Fatal("finished packs must never be swept, whatever their age")
	}
}

func TestCleanupAbortedSnapshot(t *testing.T) {
	gitDir := t.TempDir()
	packDir := filepath.Join(gitDir, "objects", "pack")
	if err := os.MkdirAll(packDir, 0o755); err != nil {
		t.Fatal(err)
	}
	started := time.Now().Add(-10 * time.Second)
	before := started.Add(-time.Minute)

	foreignLock := filepath.Join(gitDir, "index.lock")
	writeWorkspaceFile(t, gitDir, "index.lock", "")
	if err := os.Chtimes(foreignLock, before, before); err != nil {
		t.Fatal(err)
	}
	cleanupAbortedSnapshot(gitDir, started)
	if _, err := os.Stat(foreignLock); err != nil {
		t.Fatal("a lock predating our attempt must be left alone")
	}

	if err := os.Chtimes(foreignLock, time.Now(), time.Now()); err != nil {
		t.Fatal(err)
	}
	writeWorkspaceFile(t, packDir, "tmp_pack_ours", "x")
	cleanupAbortedSnapshot(gitDir, started)
	if _, err := os.Stat(foreignLock); err == nil {
		t.Fatal("our own lock must be removed after a killed run")
	}
	if _, err := os.Stat(filepath.Join(packDir, "tmp_pack_ours")); err == nil {
		t.Fatal("our own tmp pack must be removed after a killed run")
	}
}

func TestResolveCheckpointTimeout(t *testing.T) {
	cases := []struct {
		env  string
		want time.Duration
	}{
		{"", checkpointAutoTimeout},
		{"30", 30 * time.Second},
		{"0", checkpointAutoTimeout},
		{"-5", checkpointAutoTimeout},
		{"potato", checkpointAutoTimeout},
		{"99999", checkpointMaxTimeout},
	}
	for _, tc := range cases {
		t.Setenv(checkpointTimeoutEnv, tc.env)
		if got := resolveCheckpointTimeout(checkpointAutoTimeout); got != tc.want {
			t.Fatalf("env %q: got %v, want %v", tc.env, got, tc.want)
		}
	}
}

func TestSnapshotDeadlineKillsSlowGit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("PATH shim script is POSIX-only")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv(checkpointEnv, "on")
	t.Setenv(checkpointTimeoutEnv, "1")
	resetCheckpointTracker(t)

	// A git that never finishes, first on PATH: every shadowGit call must be
	// killed by the snapshot deadline instead of hanging the command.
	shim := t.TempDir()
	script := filepath.Join(shim, "git")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nsleep 30\n"), 0o755); err != nil { // #nosec G306 -- shim must be executable for LookPath to resolve it
		t.Fatal(err)
	}
	t.Setenv("PATH", shim+string(os.PathListSeparator)+os.Getenv("PATH"))

	var out bytes.Buffer
	work := t.TempDir()
	e := NewEngine(&out, &out, work)

	start := time.Now()
	e.autoCheckpoint(context.Background(), "exec")
	elapsed := time.Since(start)

	if elapsed > 5*time.Second {
		t.Fatalf("snapshot deadline did not bound the run: took %v", elapsed)
	}
	if !strings.Contains(out.String(), "checkpoint skipped") {
		t.Fatalf("a killed snapshot must be reported:\n%s", out.String())
	}
}
