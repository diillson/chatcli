/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/update"
	"github.com/diillson/chatcli/version"
)

// withRestartSeams stubs every way /restart reaches the OS: argv, PATH
// lookup, the running executable and the exec itself. The returned pointer
// records the exec call (nil when none happened).
type restartCall struct {
	bin  string
	argv []string
	env  []string
}

func withRestartSeams(t *testing.T, args []string, lookPath func(string) (string, error), exe string, execErr error) **restartCall {
	t.Helper()
	origLook, origExe, origExec, origArgs, origDir := restartLookPath, restartExecutable, restartExecFn, restartArgs, launchDir
	t.Cleanup(func() {
		restartLookPath, restartExecutable, restartExecFn, restartArgs, launchDir = origLook, origExe, origExec, origArgs, origDir
		setPendingRestart(nil)
	})
	var got *restartCall
	restartArgs = func() []string { return args }
	restartLookPath = lookPath
	restartExecutable = func() (string, error) { return exe, nil }
	restartExecFn = func(bin string, argv, env []string) error {
		got = &restartCall{bin: bin, argv: argv, env: env}
		return execErr
	}
	return &got
}

func writeFakeBinary(t *testing.T, dir, name string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return p
}

func handoffFromEnv(t *testing.T, env []string) restartHandoff {
	t.Helper()
	var h restartHandoff
	found := 0
	for _, kv := range env {
		if raw, ok := strings.CutPrefix(kv, restartHandoffEnv+"="); ok {
			found++
			if err := json.Unmarshal([]byte(raw), &h); err != nil {
				t.Fatalf("handoff is not JSON: %v", err)
			}
		}
	}
	if found != 1 {
		t.Fatalf("expected exactly one %s entry, got %d", restartHandoffEnv, found)
	}
	return h
}

// A bare command name resolves through PATH — on Homebrew that is the
// symlink already pointing at the new Cellar, never the old running file.
func TestResolveRestartBinaryBareNameUsesPath(t *testing.T) {
	dir := t.TempDir()
	onPath := writeFakeBinary(t, dir, "chatcli")
	withRestartSeams(t, nil, func(name string) (string, error) {
		if name != "chatcli" {
			t.Fatalf("lookup of %q", name)
		}
		return onPath, nil
	}, "/old/Cellar/chatcli/1.0.0/bin/chatcli", nil)

	got, err := resolveRestartBinary("chatcli")
	if err != nil || got != onPath {
		t.Fatalf("got %q, %v; want %q", got, err, onPath)
	}
}

// A relative path is resolved against the launch directory, not wherever
// the session moved since.
func TestResolveRestartBinaryRelativePathUsesLaunchDir(t *testing.T) {
	dir := t.TempDir()
	bin := writeFakeBinary(t, dir, "chatcli")
	withRestartSeams(t, nil, func(string) (string, error) { return "", errors.New("not on PATH") }, "", nil)
	launchDir = dir

	got, err := resolveRestartBinary("./chatcli")
	if err != nil || got != filepath.Join(dir, "chatcli") {
		t.Fatalf("got %q, %v; want %q", got, err, bin)
	}
}

// When argv[0] no longer resolves, the running executable is the fallback;
// when that is gone too, the restart fails instead of exec'ing nothing.
func TestResolveRestartBinaryFallbacks(t *testing.T) {
	dir := t.TempDir()
	exe := writeFakeBinary(t, dir, "chatcli-exe")
	withRestartSeams(t, nil, func(string) (string, error) { return "", errors.New("not on PATH") }, exe, nil)

	got, err := resolveRestartBinary("chatcli")
	if err != nil || got != exe {
		t.Fatalf("got %q, %v; want fallback %q", got, err, exe)
	}

	restartExecutable = func() (string, error) { return filepath.Join(dir, "gone"), nil }
	if _, err := resolveRestartBinary(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("a vanished binary must fail the restart")
	}
}

func TestRestartEnvReplacesStaleHandoff(t *testing.T) {
	env := restartEnv([]string{"A=1", restartHandoffEnv + `={"session":"old"}`, "B=2"},
		restartHandoff{Session: "new", From: "1.0.0"})
	h := handoffFromEnv(t, env)
	if h.Session != "new" || h.From != "1.0.0" {
		t.Fatalf("handoff = %+v", h)
	}
	if env[0] != "A=1" || env[1] != "B=2" {
		t.Fatalf("other variables must be kept in order: %v", env)
	}
}

// No /restart: the deferred call in main is a no-op.
func TestExecPendingRestartNoPlanIsNoop(t *testing.T) {
	called := withRestartSeams(t, []string{"chatcli"}, exec404, "", nil)
	ExecPendingRestart()
	if *called != nil {
		t.Fatal("exec without a pending restart")
	}
}

func exec404(string) (string, error) { return "", errors.New("not found") }

// The full path: /restart in the REPL saves the conversation, unwinds like
// /exit, and the deferred ExecPendingRestart starts the installed binary
// with the same argv and the handoff naming the saved session.
func TestRestartCommandSchedulesExecWithSession(t *testing.T) {
	c := bindingTestCLI(t)
	t.Setenv("CHATCLI_SESSION_AUTOSAVE", "")
	c.replActive = true
	c.history = []models.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "oi"},
		{Role: "assistant", Content: "olá"},
	}
	dir := t.TempDir()
	bin := writeFakeBinary(t, dir, "chatcli")
	args := []string{"chatcli", "--provider", "OPENAI"}
	called := withRestartSeams(t, args, func(string) (string, error) { return bin, nil }, "", nil)

	var exit bool
	captureStdout(t, func() { exit = c.handleRestartCommand(context.Background(), "/restart") })
	if !exit {
		t.Fatal("/restart must leave the REPL")
	}
	if !c.sessionAutosaved {
		t.Fatal("the exit cleanup must not write a second autosave")
	}

	ExecPendingRestart()
	got := *called
	if got == nil {
		t.Fatal("no exec happened")
	}
	if got.bin != bin || strings.Join(got.argv, " ") != strings.Join(args, " ") {
		t.Fatalf("exec %q %v", got.bin, got.argv)
	}
	h := handoffFromEnv(t, got.env)
	if !strings.HasPrefix(h.Session, autosavePrefix) || h.From != version.Version {
		t.Fatalf("handoff = %+v", h)
	}
	if !c.sessionManager.SessionExists(h.Session) {
		t.Fatalf("session %q must be on disk before the exec", h.Session)
	}
	if takePendingRestart() != nil {
		t.Fatal("the plan must be consumed once")
	}
}

// A named session is saved under its own name, not as an autosave copy.
func TestRestartKeepsNamedSession(t *testing.T) {
	c := bindingTestCLI(t)
	c.currentSessionName = "my-work"
	c.history = []models.Message{{Role: "user", Content: "oi"}}
	if got := c.persistSessionForRestart(); got != "my-work" {
		t.Fatalf("got %q", got)
	}
	if !c.sessionManager.SessionExists("my-work") {
		t.Fatal("named session not saved")
	}
}

// Nothing said yet: restart fresh, no empty autosave file.
func TestRestartEmptyConversationSavesNothing(t *testing.T) {
	c := bindingTestCLI(t)
	c.history = []models.Message{{Role: "system", Content: "sys"}}
	if got := c.persistSessionForRestart(); got != "" {
		t.Fatalf("got %q", got)
	}
}

// Headless surfaces (scheduler, ACP, web) never restart the process.
func TestRestartRefusedOutsideREPL(t *testing.T) {
	c := bindingTestCLI(t)
	withRestartSeams(t, nil, exec404, "", nil)
	var exit bool
	out := captureStdout(t, func() { exit = c.handleRestartCommand(context.Background(), "/restart") })
	if exit || takePendingRestart() != nil {
		t.Fatal("restart must not be scheduled outside the REPL")
	}
	if !strings.Contains(out, "/restart") {
		t.Fatalf("expected an explanation, got %q", out)
	}
}

// A failed exec says so and tells how to get the conversation back.
func TestExecPendingRestartFailureHint(t *testing.T) {
	dir := t.TempDir()
	bin := writeFakeBinary(t, dir, "chatcli")
	withRestartSeams(t, []string{"chatcli"}, func(string) (string, error) { return bin, nil }, "", errors.New("boom"))
	setPendingRestart(&restartPlan{handoff: restartHandoff{Session: "autosave-x"}})

	out := captureStderr(t, ExecPendingRestart)
	if !strings.Contains(out, "boom") || !strings.Contains(out, "/session load autosave-x") {
		t.Fatalf("stderr = %q", out)
	}
}

// The new process announces the restart, reloads the session and clears
// the handoff so nothing it spawns inherits it.
func TestResumeAfterRestartLoadsSession(t *testing.T) {
	c := bindingTestCLI(t)
	sd := &SessionData{Version: 2, ChatHistory: []models.Message{
		{Role: "user", Content: "antes do restart"},
		{Role: "assistant", Content: "ok"},
	}}
	if err := c.sessionManager.SaveSessionV2("autosave-1", sd); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(restartHandoff{Session: "autosave-1", From: "0.0.1-old"})
	t.Setenv(restartHandoffEnv, string(raw))

	var ok bool
	out := captureStdout(t, func() { ok = c.resumeAfterRestart(context.Background()) })
	if !ok {
		t.Fatal("handoff not detected")
	}
	if _, present := os.LookupEnv(restartHandoffEnv); present {
		t.Fatal("handoff must be cleared after boot")
	}
	if !strings.Contains(out, "0.0.1-old") {
		t.Fatalf("expected the version transition, got %q", out)
	}
	if c.currentSessionName != "autosave-1" || len(c.history) == 0 {
		t.Fatalf("session not resumed: name=%q history=%d", c.currentSessionName, len(c.history))
	}
}

func TestResumeAfterRestartNormalBoot(t *testing.T) {
	c := bindingTestCLI(t)
	if c.resumeAfterRestart(context.Background()) {
		t.Fatal("a normal boot has no handoff")
	}
}

// After an applied /update, the REPL offers /restart.
func TestUpdateAppliedInvitesRestart(t *testing.T) {
	c := minimalCLI(t)
	c.replActive = true
	withUpdateSeams(t, update.MethodHomebrew, "1.5.0", "1.6.0", nil)
	out := captureStdout(t, func() { c.handleUpdateCommand(context.Background(), "/update") })
	if !strings.Contains(out, "/restart") || !strings.Contains(out, "1.6.0") {
		t.Fatalf("expected the /restart invite, got %q", out)
	}
}

// A version staged in the background mid-session replaces the stale
// "available" notice with the /restart invite.
func TestStagedNoticeOffersRestart(t *testing.T) {
	c := minimalCLI(t)
	c.replActive = true
	c.queueUpdateNotice("1.6.0")
	c.queueRestartNotice("1.6.0")
	out := captureStdout(t, c.drainUpdateNotice)
	if !strings.Contains(out, "/restart") {
		t.Fatalf("expected the /restart invite, got %q", out)
	}
	if strings.Contains(out, "/update") {
		t.Fatalf("the stale /update notice must be dropped, got %q", out)
	}
	if out2 := captureStdout(t, c.drainUpdateNotice); strings.TrimSpace(out2) != "" {
		t.Fatalf("notices print once, got %q", out2)
	}
}

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stderr = w
	done := make(chan struct{})
	var buf bytes.Buffer
	go func() {
		_, _ = io.Copy(&buf, r)
		close(done)
	}()
	fn()
	_ = w.Close()
	os.Stderr = orig
	<-done
	return buf.String()
}
