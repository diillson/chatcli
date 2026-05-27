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
	"strconv"
	"strings"
	"testing"

	"go.uber.org/zap"
)

func TestGatewayCleanLine(t *testing.T) {
	cases := map[string]string{
		"  hello  ": "hello",
		"":          "",
		"   ":       "",
		"┌──────────────┐":    "",             // pure box-drawing -> dropped
		"│ Step 1: read │":    "Step 1: read", // box borders stripped, content kept
		"●●●":                 "",             // spinner glyphs (no alnum) -> dropped
		"running go build...": "running go build...",
		"\r  done":            "done", // carriage return stripped
	}
	for in, want := range cases {
		if got := gatewayCleanLine(in); got != want {
			t.Errorf("gatewayCleanLine(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestGatewaySessions(t *testing.T) {
	s := newGatewaySessions(2)
	if s.preamble("a") != "" {
		t.Error("new session should have empty preamble")
	}
	s.remember("a", "first")
	s.remember("a", "second")
	s.remember("a", "third") // evicts "first" (cap 2)

	pre := s.preamble("a")
	if strings.Contains(pre, "first") {
		t.Errorf("oldest request should be evicted: %q", pre)
	}
	if !strings.Contains(pre, "second") || !strings.Contains(pre, "third") {
		t.Errorf("preamble missing recent requests: %q", pre)
	}
	// Blank input is ignored, and unrelated sessions stay isolated.
	s.remember("a", "   ")
	if strings.Count(s.preamble("a"), "\n- ") != 2 {
		t.Errorf("blank request should not be stored: %q", s.preamble("a"))
	}
	if s.preamble("b") != "" {
		t.Error("sessions must be isolated")
	}
}

func TestGatewayAgentFunc_NoClient(t *testing.T) {
	c := &ChatCLI{} // Client is nil
	fn := c.gatewayAgentFunc(newGatewaySessions(5))
	if _, err := fn(context.Background(), "s", "hi"); err == nil {
		t.Error("expected error when no active model")
	}
}

func TestSetUnattended(t *testing.T) {
	c := &ChatCLI{}
	c.SetUnattended(true)
	if !c.unattended {
		t.Error("SetUnattended(true) should set the flag")
	}
}

func TestGatewayProcessAlive(t *testing.T) {
	if !gatewayProcessAlive(os.Getpid()) {
		t.Error("current process should be alive")
	}
	if gatewayProcessAlive(1 << 30) { // implausible pid
		t.Error("bogus pid should not be alive")
	}
}

func TestGatewayRunningPID(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	_ = os.MkdirAll(filepath.Join(tmp, ".chatcli"), 0o750)

	// No pidfile yet.
	if _, ok := gatewayRunningPID(); ok {
		t.Error("no pidfile -> not running")
	}
	// Stale pidfile (dead pid) is cleared and reported as not running.
	pidPath := gatewayStatePath("gateway.pid")
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(1<<30)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := gatewayRunningPID(); ok {
		t.Error("dead pid -> not running")
	}
	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Error("stale pidfile should be removed")
	}
	// Live pid (ourselves) is reported running.
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		t.Fatal(err)
	}
	if pid, ok := gatewayRunningPID(); !ok || pid != os.Getpid() {
		t.Errorf("live pid should be running, got pid=%d ok=%v", pid, ok)
	}
}

// TestGatewayCommands_NoPanic exercises the status/stop/start branches with no
// platforms configured and no daemon running (temp HOME), covering the command
// plumbing without spawning anything.
func TestGatewayCommands_NoPanic(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	// Ensure no gateway adapters are configured.
	for _, e := range []string{"CHATCLI_TELEGRAM_BOT_TOKEN", "CHATCLI_DISCORD_BOT_TOKEN", "CHATCLI_SLACK_ADDR", "CHATCLI_WHATSAPP_ADDR", "CHATCLI_WEBHOOK_ADDR"} {
		t.Setenv(e, "")
	}
	c := &ChatCLI{logger: zap.NewNop()}

	c.gatewayStatus()        // not running + 0 configured
	c.gatewayStop()          // not running branch
	c.gatewayStartDetached() // no adapters -> prints and returns
	if err := c.RunGatewayForeground(context.Background()); err == nil {
		t.Error("RunGatewayForeground with no adapters should error")
	}
	if !c.unattended {
		t.Error("RunGatewayForeground should set unattended")
	}
}

func TestStripCommandBlocksText(t *testing.T) {
	blocks := []CommandBlock{{Language: "shell", Commands: []string{"ls"}, Description: "list"}}
	in := "Here is the plan.\n```execute:shell\nls```\nDone."
	out := stripCommandBlocksText(in, blocks)
	if strings.Contains(out, "```execute") {
		t.Errorf("execute block should be replaced: %q", out)
	}
	if !strings.Contains(out, "[Comando #1: list]") || !strings.Contains(out, "Here is the plan.") {
		t.Errorf("placeholder/prose missing: %q", out)
	}
}

func TestReadLine_Unattended(t *testing.T) {
	a := &AgentMode{cli: &ChatCLI{unattended: true}}
	if got := a.readLine(); got != unattendedConfirmAnswer {
		t.Errorf("unattended readLine should auto-confirm, got %q", got)
	}
}
