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
 * git is absent.
 */
package engine

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
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
	checkpointMu.Lock()
	lastCheckpoint = map[string]time.Time{}
	checkpointMu.Unlock()

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

func resetCheckpointThrottle() {
	checkpointMu.Lock()
	lastCheckpoint = map[string]time.Time{}
	checkpointMu.Unlock()
}

func TestAutoCheckpointAndRestore(t *testing.T) {
	e, out, work := checkpointTestEngine(t)
	ctx := context.Background()

	writeWorkspaceFile(t, work, "app.go", "package main // v1\n")
	if err := snapshotWorkspace(work, "seed"); err != nil {
		t.Fatalf("seed snapshot: %v", err)
	}

	resetCheckpointThrottle()
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
	hash, err := shadowGit(gitDir, work, "rev-list", "--max-count=1", "--reverse", "HEAD")
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
	resetCheckpointThrottle()
	writeWorkspaceFile(t, work, "x.go", "package x\n")
	e.autoCheckpoint("write")
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

	resetCheckpointThrottle()
	e.autoCheckpoint("write")

	data, err := os.ReadFile(filepath.Join(userGit, "CANARY"))
	if err != nil || !strings.Contains(string(data), "do-not-touch") {
		t.Fatalf("user .git was disturbed: %v", err)
	}
	gitDir, _ := shadowGitDir(work)
	if !strings.Contains(gitDir, ".chatcli") {
		t.Fatalf("shadow git dir not under chatcli home: %s", gitDir)
	}
}
