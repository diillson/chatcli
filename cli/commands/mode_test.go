/*
 * ChatCLI - Execution mode resolution tests.
 * Copyright (c) 2024 Edilson Freitas. License: Apache-2.0.
 */
package commands

import (
	"path/filepath"
	"testing"
)

func TestResolvedMode_ExplicitCoder(t *testing.T) {
	c := &Command{Mode: "coder"}
	if got := c.ResolvedMode(); got != ExecModeCoder {
		t.Errorf("mode: coder must resolve to coder, got %q", got)
	}
}

func TestResolvedMode_ExplicitChatBeatsAllowedTools(t *testing.T) {
	c := &Command{Mode: "chat", AllowedTools: []string{"exec_command"}}
	if got := c.ResolvedMode(); got != ExecModeChat {
		t.Errorf("explicit mode: chat must veto the allowed-tools inference, got %q", got)
	}
}

func TestResolvedMode_AgentAliasesCoder(t *testing.T) {
	c := &Command{Mode: "Agent"} // case-insensitive on purpose
	if got := c.ResolvedMode(); got != ExecModeCoder {
		t.Errorf("mode: agent must alias coder (same ReAct engine), got %q", got)
	}
}

func TestResolvedMode_InferredFromAllowedTools(t *testing.T) {
	c := &Command{AllowedTools: []string{"exec_command", "write_file"}}
	if got := c.ResolvedMode(); got != ExecModeCoder {
		t.Errorf("allowed-tools without mode must infer coder, got %q", got)
	}
}

func TestResolvedMode_DefaultChat(t *testing.T) {
	c := &Command{}
	if got := c.ResolvedMode(); got != ExecModeChat {
		t.Errorf("bare command must default to chat, got %q", got)
	}
}

func TestResolvedMode_InvalidValueFallsBackToInference(t *testing.T) {
	withTools := &Command{Mode: "banana", AllowedTools: []string{"exec_command"}}
	if got := withTools.ResolvedMode(); got != ExecModeCoder {
		t.Errorf("unknown mode with allowed-tools must fall back to inference (coder), got %q", got)
	}
	plain := &Command{Mode: "banana"}
	if got := plain.ResolvedMode(); got != ExecModeChat {
		t.Errorf("unknown mode without tools must fall back to chat, got %q", got)
	}
}

func TestParseExecutionMode_Tolerance(t *testing.T) {
	cases := map[string]ExecutionMode{
		"coder":   ExecModeCoder,
		"  CODER": ExecModeCoder,
		"agent":   ExecModeCoder,
		"chat":    ExecModeChat,
		"Chat ":   ExecModeChat,
		"":        "",
		"banana":  "",
		"co der":  "", // invalid rune → no opinion, never an error
	}
	for raw, want := range cases {
		if got := ParseExecutionMode(raw); got != want {
			t.Errorf("ParseExecutionMode(%q) = %q, want %q", raw, got, want)
		}
	}
}

// TestParseCommandFile_ModeFrontmatter covers the three parse paths end to
// end: strict YAML, the loose line-wise fallback, and the Gemini TOML subset.
func TestParseCommandFile_ModeFrontmatter(t *testing.T) {
	cat, project, _ := newTestCatalog(t)
	write(t, filepath.Join(project, ".chatcli", "commands", "deploy.md"),
		"---\ndescription: Deploy the app\nmode: coder\n---\nRun the deploy script and report the outcome.")
	// Broken YAML (two flow scalars) forces the loose fallback path.
	write(t, filepath.Join(project, ".chatcli", "commands", "loosemode.md"),
		"---\nargument-hint: [FILES=<paths>] [PR_TITLE=\"<title>\"]\nmode: coder\n---\nbody")
	write(t, filepath.Join(project, ".gemini", "commands", "tomlmode.toml"),
		"description = \"toml mode\"\nmode = \"coder\"\nprompt = \"\"\"\ndo the thing\n\"\"\"")

	for _, name := range []string{"deploy", "loosemode", "tomlmode"} {
		cmd := cat.Get(name)
		if cmd == nil {
			t.Fatalf("%s must load; skipped=%v", name, cat.Skipped())
		}
		if cmd.Mode != "coder" {
			t.Errorf("%s: Mode = %q, want %q", name, cmd.Mode, "coder")
		}
		if cmd.ResolvedMode() != ExecModeCoder {
			t.Errorf("%s: ResolvedMode = %q, want coder", name, cmd.ResolvedMode())
		}
	}
}
