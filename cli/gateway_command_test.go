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

	"github.com/diillson/chatcli/cli/gateway"
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

// Gateway per-conversation continuity is now hub-backed; see gateway_hub_test.go
// (TestHubSessions*). The old rolling-window gatewaySessions was removed.

func TestGatewayAgentFunc_NoClient(t *testing.T) {
	c := &ChatCLI{} // Client is nil
	fn := c.gatewayAgentFunc(newHubSessions(nil, zap.NewNop()))
	msg := gateway.InboundMessage{Platform: "telegram", ChatID: "1", UserID: "u", Text: "hi"}
	if _, err := fn(context.Background(), msg); err == nil {
		t.Error("expected error when no active model")
	}
}

// TestRunCoderOnce_InvalidInput pins the CutPrefix refactor: only "/coder <q>"
// is a valid one-shot invocation; bare "/coder" or anything without the prefix
// must error before touching the (nil) LLM client.
func TestRunCoderOnce_InvalidInput(t *testing.T) {
	c := &ChatCLI{}
	for _, in := range []string{"/coder", "no prefix", "/coderx hi", ""} {
		if err := c.RunCoderOnce(context.Background(), in); err == nil {
			t.Errorf("RunCoderOnce(%q) should error on invalid input", in)
		}
	}
}

func TestSetUnattended(t *testing.T) {
	c := &ChatCLI{}
	c.SetUnattended(true)
	if !c.unattended {
		t.Error("SetUnattended(true) should set the flag")
	}
}

// TestSetUnattended_SuppressesSpinner pins the root-cause fix: in the gateway
// daemon stdout is a captured pipe, and the thinking-spinner frames
// (`model... |/-\`) carry alphanumerics so they slip past gatewayCleanLine and
// flood the action feed. SetUnattended must suppress the animation at the
// source so no frame is ever produced. A nil animation must be tolerated.
func TestSetUnattended_SuppressesSpinner(t *testing.T) {
	c := &ChatCLI{animation: NewAnimationManager()}
	c.SetUnattended(true)
	if !c.animation.suppressed {
		t.Error("SetUnattended(true) should suppress the thinking animation")
	}
	// Suppressed: ShowThinkingAnimation must not start the spinner goroutine.
	c.animation.ShowThinkingAnimation("Claude sonnet 4.6 (1M context)")
	if c.animation.isRunning {
		t.Error("suppressed animation must not start the spinner goroutine")
	}
	c.SetUnattended(false)
	if c.animation.suppressed {
		t.Error("SetUnattended(false) should re-enable the animation")
	}
	// Nil animation (bare struct) must not panic.
	(&ChatCLI{}).SetUnattended(true)
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

func TestHandleGatewayCommand_Dispatch(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, e := range []string{"CHATCLI_TELEGRAM_BOT_TOKEN", "CHATCLI_DISCORD_BOT_TOKEN", "CHATCLI_SLACK_ADDR", "CHATCLI_WHATSAPP_ADDR", "CHATCLI_WEBHOOK_ADDR"} {
		t.Setenv(e, "")
	}
	c := &ChatCLI{logger: zap.NewNop()}
	// Each branch must dispatch without panicking.
	c.handleGatewayCommand("/gateway")        // -> start (no adapters)
	c.handleGatewayCommand("/gateway status") // -> status
	c.handleGatewayCommand("/gateway stop")   // -> stop (not running)
	c.handleGatewayCommand("/gateway nope")   // -> usage
}

func TestGatewayStartDetached_AlreadyRunning(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	_ = os.MkdirAll(filepath.Join(tmp, ".chatcli"), 0o750)
	// A live pidfile (ourselves) makes start short-circuit on "already running".
	if err := os.WriteFile(gatewayStatePath("gateway.pid"), []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		t.Fatal(err)
	}
	c := &ChatCLI{logger: zap.NewNop()}
	c.gatewayStartDetached() // must not spawn; just reports already running
}

func TestStripCommandBlocksText(t *testing.T) {
	blocks := []CommandBlock{{Language: "shell", Commands: []string{"ls"}, Description: "list"}}
	in := "Here is the plan.\n```execute:shell\nls```\nDone."
	out := stripCommandBlocksText(in, blocks)
	if strings.Contains(out, "```execute") {
		t.Errorf("execute block should be replaced: %q", out)
	}
	if !strings.Contains(out, "[Command #1: list]") || !strings.Contains(out, "Here is the plan.") {
		t.Errorf("placeholder/prose missing: %q", out)
	}

	// Realistic model output: the closing fence sits on its own line, so there
	// is a newline before ```. The old literal-reconstruction strip missed this
	// and leaked the raw block into the gateway reply. Regex strip must catch it.
	in2 := "Tudo em paz!\n```execute:shell\necho \"Em paz! 🕊️\"\n```\n"
	out2 := stripCommandBlocksText(in2, []CommandBlock{{Language: "shell", Description: "greet"}})
	if strings.Contains(out2, "```") || strings.Contains(out2, "echo") {
		t.Errorf("trailing-newline execute block should be stripped: %q", out2)
	}
	if !strings.Contains(out2, "[Command #1: greet]") || !strings.Contains(out2, "Tudo em paz!") {
		t.Errorf("placeholder/prose missing: %q", out2)
	}
}

func TestReadLine_Unattended(t *testing.T) {
	a := &AgentMode{cli: &ChatCLI{unattended: true}}
	if got := a.readLine(); got != unattendedConfirmAnswer {
		t.Errorf("unattended readLine should auto-confirm, got %q", got)
	}
}

// TestTeeLoggerToGatewayLog verifies the daemon's logger is teed into the
// advertised gateway.log so the file is not a false promise.
func TestTeeLoggerToGatewayLog(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	c := &ChatCLI{logger: zap.NewNop()}
	closeTee := c.teeLoggerToGatewayLog()
	if closeTee == nil {
		t.Fatal("expected a non-nil closer when gateway.log is writable")
	}
	c.logger.Info("hello-gateway-log")
	closeTee()

	data, err := os.ReadFile(filepath.Join(tmp, ".chatcli", "gateway.log"))
	if err != nil {
		t.Fatalf("gateway.log not written: %v", err)
	}
	if !strings.Contains(string(data), "hello-gateway-log") {
		t.Errorf("gateway.log missing the teed entry: %s", data)
	}
}
