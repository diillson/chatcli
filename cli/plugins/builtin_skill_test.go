/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package plugins

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func withSkillsDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	old := skillsDirOverride
	skillsDirOverride = dir
	t.Cleanup(func() { skillsDirOverride = old })
	return dir
}

func TestSkill_CreateThenShowAndList(t *testing.T) {
	dir := withSkillsDir(t)
	p := NewBuiltinSkillPlugin()

	_, err := p.Execute(context.Background(), []string{`{"cmd":"create","args":{"name":"deploy-x","description":"How to deploy X","content":"# Deploy\nrun make","triggers":["deploy x","ship x"],"allowed_tools":["@coder","Bash"]}}`})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// File exists with frontmatter.
	data, err := os.ReadFile(filepath.Join(dir, "deploy-x", "SKILL.md"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	s := string(data)
	for _, want := range []string{"name: \"deploy-x\"", "description: \"How to deploy X\"", "triggers:", "- \"deploy x\"", "allowed-tools: [\"@coder\",\"Bash\"]", "# Deploy"} {
		if !strings.Contains(s, want) {
			t.Errorf("SKILL.md missing %q\n---\n%s", want, s)
		}
	}

	// list
	out, _ := p.Execute(context.Background(), []string{`{"cmd":"list"}`})
	if !strings.Contains(out, "deploy-x") {
		t.Fatalf("list missing skill: %q", out)
	}
	// show
	out, _ = p.Execute(context.Background(), []string{`{"cmd":"show","args":{"name":"deploy-x"}}`})
	if !strings.Contains(out, "# Deploy") {
		t.Fatalf("show missing content: %q", out)
	}
}

func TestSkill_CreateRejectsDuplicate(t *testing.T) {
	withSkillsDir(t)
	p := NewBuiltinSkillPlugin()
	body := `{"cmd":"create","args":{"name":"dup","description":"d","content":"c"}}`
	if _, err := p.Execute(context.Background(), []string{body}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Execute(context.Background(), []string{body}); err == nil {
		t.Fatal("expected error creating duplicate")
	}
}

func TestSkill_UpdateRequiresExisting(t *testing.T) {
	withSkillsDir(t)
	p := NewBuiltinSkillPlugin()
	if _, err := p.Execute(context.Background(), []string{`{"cmd":"update","args":{"name":"ghost","description":"d","content":"c"}}`}); err == nil {
		t.Fatal("expected error updating nonexistent skill")
	}
}

func TestSkill_Update(t *testing.T) {
	dir := withSkillsDir(t)
	p := NewBuiltinSkillPlugin()
	_, _ = p.Execute(context.Background(), []string{`{"cmd":"create","args":{"name":"k","description":"d","content":"v1"}}`})
	if _, err := p.Execute(context.Background(), []string{`{"cmd":"update","args":{"name":"k","description":"d","content":"v2 content"}}`}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "k", "SKILL.md"))
	if !strings.Contains(string(data), "v2 content") {
		t.Fatalf("update did not apply: %s", data)
	}
}

func TestSkill_Remove(t *testing.T) {
	dir := withSkillsDir(t)
	p := NewBuiltinSkillPlugin()
	_, _ = p.Execute(context.Background(), []string{`{"cmd":"create","args":{"name":"tmp","description":"d","content":"c"}}`})
	if _, err := p.Execute(context.Background(), []string{`{"cmd":"remove","args":{"name":"tmp"}}`}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "tmp")); !os.IsNotExist(err) {
		t.Fatal("skill dir should be gone")
	}
}

func TestSkill_InvalidName(t *testing.T) {
	withSkillsDir(t)
	p := NewBuiltinSkillPlugin()
	for _, bad := range []string{"../escape", "Has Space", "UPPER", "a/b"} {
		body := `{"cmd":"create","args":{"name":"` + bad + `","description":"d","content":"c"}}`
		if _, err := p.Execute(context.Background(), []string{body}); err == nil {
			t.Errorf("expected rejection of name %q", bad)
		}
	}
}

func TestCanonicalSkillCmd(t *testing.T) {
	if canonicalSkillCmd("author") != "create" || canonicalSkillCmd("evolve") != "update" {
		t.Fatal("create/update aliases wrong")
	}
	if canonicalSkillCmd("rm") != "remove" || canonicalSkillCmd("view") != "show" {
		t.Fatal("remove/show aliases wrong")
	}
	if canonicalSkillCmd("zz") != "" {
		t.Fatal("unknown should be empty")
	}
}
