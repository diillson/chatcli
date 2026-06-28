/*
 * ChatCLI - Tests for the chat-mode @graphview exception.
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

	"github.com/diillson/chatcli/models"
)

func TestChatGraphViewEnabledDefaultAndToggle(t *testing.T) {
	t.Setenv(chatGraphViewEnvVar, "")
	if !chatGraphViewEnabled() {
		t.Fatal("default should be ON")
	}
	t.Setenv(chatGraphViewEnvVar, "off")
	if chatGraphViewEnabled() {
		t.Fatal("off must disable")
	}
	t.Setenv(chatGraphViewEnvVar, "true")
	if !chatGraphViewEnabled() {
		t.Fatal("true must enable")
	}
}

func TestGraphViewToolDefinition(t *testing.T) {
	def := graphViewToolDefinition()
	if def.Function.Name != "graphview" {
		t.Fatalf("name = %q", def.Function.Name)
	}
	if def.Function.Parameters == nil {
		t.Fatal("missing parameters")
	}
}

func TestIsGraphViewToolName(t *testing.T) {
	for _, n := range []string{"graphview", "@graphview", "  GraphView  "} {
		if !isGraphViewToolName(n) {
			t.Fatalf("%q should match", n)
		}
	}
	if isGraphViewToolName("knowledge") {
		t.Fatal("knowledge must not match")
	}
}

func TestAppendGraphViewRound(t *testing.T) {
	hist := []models.Message{{Role: "user", Content: "x"}}
	next, followup := appendGraphViewRound(hist, "prompt", `{"source":"json"}`, "ok")
	if len(next) != len(hist)+2 {
		t.Fatalf("history len = %d", len(next))
	}
	if !strings.Contains(followup, "graphview result") {
		t.Fatalf("followup = %q", followup)
	}
}

func TestChatGraphViewXMLInstruction(t *testing.T) {
	s := chatGraphViewXMLInstruction()
	if !strings.Contains(s, "@graphview") {
		t.Fatalf("instruction missing tool name: %q", s)
	}
}

func TestRunChatGraphView(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "g.html")
	cli := &ChatCLI{}
	args := `{"output":"` + out + `","open":false,"nodes":[{"id":"a","label":"A"}],"edges":[]}`
	res := cli.runChatGraphView(context.Background(), args)
	if strings.HasPrefix(res, "graphview error") {
		t.Fatalf("unexpected error: %s", res)
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatalf("output not written: %v", err)
	}
}

func TestConfigChatGraphViewToggle(t *testing.T) {
	t.Setenv(chatGraphViewEnvVar, "true")
	cli := &ChatCLI{}
	cli.configChatGraphView([]string{"off"})
	if chatGraphViewEnabled() {
		t.Fatal("toggle off did not take effect")
	}
	cli.configChatGraphView([]string{"on"})
	if !chatGraphViewEnabled() {
		t.Fatal("toggle on did not take effect")
	}
	// invalid value path (prints guidance, must not panic)
	cli.configChatGraphView([]string{"bogus"})
}

func TestShowConfigGraphViewRuns(t *testing.T) {
	// Smoke: must render without panicking on a bare CLI.
	(&ChatCLI{}).showConfigGraphView()
}
