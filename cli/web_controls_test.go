/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"strings"
	"testing"

	"github.com/diillson/chatcli/cli/mcp"
	"github.com/diillson/chatcli/i18n"
	"go.uber.org/zap"
)

func webControlsCLI(t *testing.T) *ChatCLI {
	t.Helper()
	c, _ := newPipelineCLI(t, map[string]string{
		"alpha":  "---\nname: alpha\ndescription: an ordinary skill\n---\nalpha body\n",
		"deploy": "---\nname: deploy\ndescription: manual only\ndisable-model-invocation: true\n---\ndeploy body\n",
	})
	return c
}

// The page's skill switch is /skill pin and /skill unpin: the same pinned
// set, so a skill pinned in the browser joins the turns the terminal's own
// pin would, and the switch reports the state the terminal lists.
func TestSetSkillPinnedRPC_SharesThePinnedSetWithSkillPin(t *testing.T) {
	c := webControlsCLI(t)

	if err := c.SetSkillPinnedRPC("alpha", true); err != nil {
		t.Fatalf("pin: %v", err)
	}
	if !c.skillHandler.IsPinned("alpha") {
		t.Fatal("alpha must be in the pinned set /skill pinned lists")
	}
	if got := c.skillHandler.GetPinnedSkills(); len(got) != 1 || got[0].Name != "alpha" {
		t.Fatalf("the turn pipeline must see alpha pinned, got %+v", got)
	}
	pinned, manual := c.WebSkillStateRPC()
	if len(pinned) != 1 || pinned[0] != "alpha" || len(manual) != 1 || manual[0] != "deploy" {
		t.Fatalf("state: pinned %v, manual-only %v", pinned, manual)
	}
	// Pinning twice is not an error, as in the terminal.
	if err := c.SetSkillPinnedRPC("alpha", true); err != nil {
		t.Fatalf("second pin: %v", err)
	}

	if err := c.SetSkillPinnedRPC("alpha", false); err != nil {
		t.Fatalf("unpin: %v", err)
	}
	if c.skillHandler.IsPinned("alpha") {
		t.Fatal("alpha must be unpinned")
	}
	// A pin made in the terminal is the one the page switches off.
	c.skillHandler.Pin("alpha")
	if err := c.SetSkillPinnedRPC("alpha", false); err != nil || c.skillHandler.IsPinned("alpha") {
		t.Fatalf("unpinning a terminal pin: err %v, still pinned %v", err, c.skillHandler.IsPinned("alpha"))
	}
}

func TestSetSkillPinnedRPC_RefusesWhatSkillPinRefuses(t *testing.T) {
	c := webControlsCLI(t)

	err := c.SetSkillPinnedRPC("deploy", true)
	if err == nil || !strings.Contains(err.Error(), i18n.T("skill.pin.disabled_invocation")) {
		t.Fatalf("manual-only skill: got %v", err)
	}
	if c.skillHandler.IsPinned("deploy") {
		t.Fatal("a manual-only skill must not be pinned")
	}
	err = c.SetSkillPinnedRPC("ghost", true)
	if err == nil || !strings.Contains(err.Error(), i18n.T("skill.pin.not_found")) {
		t.Fatalf("unknown skill: got %v", err)
	}
	if err := c.SetSkillPinnedRPC("  ", true); err == nil {
		t.Fatal("an empty name must be refused")
	}
	if err := (&ChatCLI{logger: zap.NewNop()}).SetSkillPinnedRPC("alpha", true); err == nil {
		t.Fatal("no skill handler: want an error")
	}
}

func TestSetMCPServerRunningRPC_ReportsTheTerminalErrors(t *testing.T) {
	if err := (&ChatCLI{}).SetMCPServerRunningRPC("fs", true); err == nil || err.Error() != i18n.T("mcp.cmd.not_enabled") {
		t.Fatalf("no MCP manager: got %v", err)
	}

	c := &ChatCLI{logger: zap.NewNop(), mcpManager: mcp.NewManagerWithOptions(zap.NewNop(), mcp.ChannelManagerOptions{PersistDir: t.TempDir()})}
	if err := c.SetMCPServerRunningRPC("", true); err == nil {
		t.Fatal("an empty name must be refused")
	}
	for _, on := range []bool{true, false} {
		err := c.SetMCPServerRunningRPC("ghost", on)
		want := i18n.T("mcp.cmd.unknown_server", "ghost")
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("on=%v unknown server: got %v, want it to contain %q", on, err, want)
		}
	}
}

// The page completes a command's arguments from the REPL completer, for
// the commands it runs, and leaves the command name to its own list.
func TestWebCompleteRPC_UsesTheREPLCompleterForWebCommands(t *testing.T) {
	c := webControlsCLI(t)

	got := c.WebCompleteRPC("/mcp ")
	seen := map[string]string{}
	for _, s := range got {
		seen[s.Text] = s.Description
	}
	for _, want := range []string{"status", "start", "stop", "restart", "login"} {
		if seen[want] == "" {
			t.Errorf("/mcp completion lacks %q with a description; got %+v", want, got)
		}
	}
	// The word being typed narrows the list, as in the terminal.
	for _, s := range c.WebCompleteRPC("/mcp st") {
		if !strings.HasPrefix(s.Text, "st") {
			t.Errorf("/mcp st offered %q", s.Text)
		}
	}
	if got := c.WebCompleteRPC("/skill pin "); len(got) == 0 {
		t.Error("/skill pin should offer the skills it can pin")
	}

	for _, line := range []string{"/mcp", "/mc", "hello /mcp ", "", "/exit ", "/update ", "/worktree "} {
		if got := c.WebCompleteRPC(line); len(got) != 0 {
			t.Errorf("%q must get no completions here, got %+v", line, got)
		}
	}
}
