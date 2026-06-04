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
	"strings"
	"testing"

	"github.com/diillson/chatcli/cli/plugins"
)

// End-to-end regression for the reported bug: the agent flattens a {cmd,args}
// tool envelope into "--flag value" argv via parseToolArgsWithJSON, and the new
// builtins must parse that. This drives the REAL flattener into the REAL plugin.
func TestAgentFlatten_SkillCreateE2E(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)        // resolveSkillsDir → $HOME/.chatcli/skills (unix)
	t.Setenv("USERPROFILE", home) // windows

	argLine := `{"cmd":"create","args":{"name":"deploy-x","description":"How to deploy X","content":"# Deploy\nmake build","triggers":["deploy x","ship x"],"allowed_tools":["@coder","Bash"]}}`
	argv, err := parseToolArgsWithJSON(argLine)
	if err != nil {
		t.Fatalf("flatten: %v", err)
	}
	// Sanity: the flattener produced argv, not the JSON envelope.
	if len(argv) == 0 || argv[0] != "create" {
		t.Fatalf("unexpected flattened argv: %v", argv)
	}

	out, err := plugins.NewBuiltinSkillPlugin().Execute(context.Background(), argv)
	if err != nil {
		t.Fatalf("@skill create via flattened argv failed: %v\nargv=%v", err, argv)
	}
	_ = out

	path := filepath.Join(home, ".chatcli", "skills", "deploy-x", "SKILL.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("skill not written at %s: %v", path, err)
	}
	s := string(data)
	for _, want := range []string{`name: "deploy-x"`, `- "deploy x"`, `- "ship x"`, `allowed-tools: ["@coder","Bash"]`, "# Deploy"} {
		if !strings.Contains(s, want) {
			t.Errorf("SKILL.md missing %q\n---\n%s", want, s)
		}
	}
}

// Same chain for @session via its adapter (proves --query is parsed, not taken
// literally as "--query ...").
type flattenFakeSession struct{ q string }

func (f *flattenFakeSession) Search(_ context.Context, query string, _ int) (string, error) {
	f.q = query
	return "ok", nil
}
func (f *flattenFakeSession) List(context.Context) (string, error) { return "", nil }

func TestAgentFlatten_SessionSearchE2E(t *testing.T) {
	f := &flattenFakeSession{}
	plugins.SetSessionAdapter(f)
	t.Cleanup(func() { plugins.SetSessionAdapter(nil) })

	argv, err := parseToolArgsWithJSON(`{"cmd":"search","args":{"query":"chapolin colorado"}}`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plugins.NewBuiltinSessionPlugin().Execute(context.Background(), argv); err != nil {
		t.Fatal(err)
	}
	if f.q != "chapolin colorado" {
		t.Fatalf("query = %q (flatten/parse regression)", f.q)
	}
}
