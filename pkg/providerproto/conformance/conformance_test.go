/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package conformance

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func build(t *testing.T, pkg string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "plugin")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	out, err := exec.Command("go", "build", "-o", bin, pkg).CombinedOutput()
	if err != nil {
		t.Fatalf("go build %s: %v\n%s", pkg, err, out)
	}
	return bin
}

func statuses(r *Report) map[string]Check {
	m := map[string]Check{}
	for _, c := range r.Checks {
		m[c.ID] = c
	}
	return m
}

func TestEchoPluginPassesEveryCheck(t *testing.T) {
	bin := build(t, "github.com/diillson/chatcli/pkg/providersdk/examples/echo")
	rep, err := Run(context.Background(), Options{
		Command: []string{bin},
		Inputs:  map[string]string{"token": "demo"},
		Timeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	got := statuses(rep)
	for _, id := range []string{
		"lifecycle.before_initialize", "initialize", "protocol.unknown_method",
		"auth.login", "auth.status", "auth.token", "auth.status_without_credential",
		"models.list", "chat.send", "chat.unknown_model", "chat.stream",
		"chat.cancel", "tokens.count", "lifecycle.exit", "protocol.stdout_clean",
	} {
		c, ok := got[id]
		if !ok {
			t.Errorf("check %s did not run", id)
			continue
		}
		if c.Status != Pass {
			t.Errorf("%s = %s (%s)", id, c.Status, c.Detail)
		}
	}
	if !rep.Passed() {
		t.Fatalf("report failed: %+v\nstderr: %s", rep.Checks, rep.Stderr)
	}
}

// Without login inputs the auth-dependent checks are skipped, never failed:
// a skip tells the author what to pass, a fail would blame the plugin.
func TestMissingInputsSkipInsteadOfFailing(t *testing.T) {
	bin := build(t, "github.com/diillson/chatcli/pkg/providersdk/examples/echo")
	rep, err := Run(context.Background(), Options{Command: []string{bin}, Timeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	got := statuses(rep)
	if c := got["auth.login"]; c.Status != Skip || !strings.Contains(c.Detail, "token") {
		t.Fatalf("auth.login = %+v, want a skip naming the token field", c)
	}
	if c := got["chat.send"]; c.Status != Skip {
		t.Fatalf("chat.send = %+v, want skip", c)
	}
	if !rep.Passed() {
		t.Fatalf("report failed: %+v", rep.Checks)
	}
}

func TestBrokenPluginIsReported(t *testing.T) {
	rep, err := Run(context.Background(), Options{
		Command: []string{os.Args[0], "-test.run=^$"},
		Env:     []string{noisyEnv + "=1"},
		Timeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Passed() {
		t.Fatal("a broken plugin passed")
	}
	got := statuses(rep)
	if c := got["initialize"]; c.Status != Fail || !strings.Contains(c.Detail, "built-in") {
		t.Errorf("initialize = %+v, want a fail for impersonating a built-in", c)
	}
	if c := got["protocol.stdout_clean"]; c.Status != Fail {
		t.Errorf("stdout_clean = %+v, want fail", c)
	}
}

func TestRunRejectsMissingBinary(t *testing.T) {
	if _, err := Run(context.Background(), Options{Command: []string{filepath.Join(t.TempDir(), "nope")}}); err == nil {
		t.Fatal("expected a start error")
	}
}
