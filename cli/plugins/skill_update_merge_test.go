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
	"reflect"
	"strings"
	"testing"

	"github.com/diillson/chatcli/pkg/persona"
	"gopkg.in/yaml.v3"
)

// richSkillMD is a hand-written skill using frontmatter renderSkill does not
// own, plus allowed-tools in the comma-separated shape the loader accepts.
const richSkillMD = `---
name: helm-chart
description: Write Helm charts the house way
allowed-tools: Read, Grep, @coder
model: opus
effort: high
version: "2.1"
user-invocable: true
paths:
  - "deploy/**/*.yaml"
  - "charts/**"
triggers:
  - helm chart
  - chart values
---

# Helm

original body
`

func writeRichSkill(t *testing.T, dir string) string {
	t.Helper()
	file := filepath.Join(dir, "helm-chart", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(richSkillMD), 0o600); err != nil {
		t.Fatal(err)
	}
	return file
}

// parseSaved reads a SKILL.md back the way the loader does.
func parseSaved(t *testing.T, file string) (persona.Skill, string) {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	fm, body, ok := splitSkillMD(string(data))
	if !ok {
		t.Fatalf("saved skill has no frontmatter:\n%s", data)
	}
	var s persona.Skill
	if err := yaml.Unmarshal([]byte(fm), &s); err != nil {
		t.Fatalf("saved frontmatter does not parse: %v\n%s", err, data)
	}
	return s, strings.TrimSpace(body)
}

// An update that sends only a new body must not erase anything else: this
// is how the self-evolution engine silently stripped allowed-tools, and a
// skill with paths would have stopped activating on file paths.
func TestSkillUpdate_KeepsEveryFieldItWasNotGiven(t *testing.T) {
	dir := withSkillsDir(t)
	file := writeRichSkill(t, dir)

	if _, err := SaveSkill(AutoSkillInput{Name: "helm-chart", Content: "# Helm\n\nevolved body"}, true); err != nil {
		t.Fatal(err)
	}
	s, body := parseSaved(t, file)
	if body != "# Helm\n\nevolved body" {
		t.Fatalf("body = %q", body)
	}
	if s.Description != "Write Helm charts the house way" {
		t.Errorf("description = %q", s.Description)
	}
	if want := []string{"Read", "Grep", "@coder"}; !reflect.DeepEqual([]string(s.Tools), want) {
		t.Errorf("allowed-tools = %v, want %v", s.Tools, want)
	}
	if want := []string{"deploy/**/*.yaml", "charts/**"}; !reflect.DeepEqual([]string(s.Paths), want) {
		t.Errorf("paths = %v, want %v", s.Paths, want)
	}
	if want := []string{"helm chart", "chart values"}; !reflect.DeepEqual([]string(s.Triggers), want) {
		t.Errorf("triggers = %v, want %v", s.Triggers, want)
	}
	if s.Model != "opus" || s.Effort != "high" || s.Version != "2.1" || !s.UserInvocable {
		t.Errorf("scalars lost: model=%q effort=%q version=%q user-invocable=%v",
			s.Model, s.Effort, s.Version, s.UserInvocable)
	}
}

// Fields the caller does send replace the stored ones; the rest stay.
func TestSkillUpdate_ReplacesWhatItWasGiven(t *testing.T) {
	dir := withSkillsDir(t)
	file := writeRichSkill(t, dir)
	p := NewBuiltinSkillPlugin()

	if _, err := p.Execute(context.Background(), []string{`{"cmd":"update","args":{"name":"helm-chart","description":"new desc","triggers":["helm"],"allowed_tools":["@coder"]}}`}); err != nil {
		t.Fatal(err)
	}
	s, body := parseSaved(t, file)
	if s.Description != "new desc" {
		t.Errorf("description = %q", s.Description)
	}
	if want := []string{"helm"}; !reflect.DeepEqual([]string(s.Triggers), want) {
		t.Errorf("triggers = %v, want %v", s.Triggers, want)
	}
	if want := []string{"@coder"}; !reflect.DeepEqual([]string(s.Tools), want) {
		t.Errorf("allowed-tools = %v, want %v", s.Tools, want)
	}
	if len(s.Paths) != 2 || s.Model != "opus" {
		t.Errorf("untouched fields lost: paths=%v model=%q", s.Paths, s.Model)
	}
	// No content sent: the stored body stays.
	if body != "# Helm\n\noriginal body" {
		t.Errorf("body = %q", body)
	}
}

// Create still renders from the input alone, and an update over a file whose
// frontmatter is broken repairs it instead of failing.
func TestSkillUpdate_BrokenFrontmatterIsRepaired(t *testing.T) {
	dir := withSkillsDir(t)
	file := filepath.Join(dir, "broken", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("---\nname: [unclosed\n---\n\nbody\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := SaveSkill(AutoSkillInput{Name: "broken", Description: "fixed", Content: "new body", Triggers: []string{"fix"}}, true); err != nil {
		t.Fatal(err)
	}
	s, body := parseSaved(t, file)
	if s.Name != "broken" || s.Description != "fixed" || body != "new body" {
		t.Fatalf("repair failed: %+v body=%q", s, body)
	}
}

// An update with nothing to fall back on still demands the required fields.
func TestSkillUpdate_StillRequiresDescriptionAndContent(t *testing.T) {
	dir := withSkillsDir(t)
	file := filepath.Join(dir, "bare", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("---\nname: bare\n---\n\nbody\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := SaveSkill(AutoSkillInput{Name: "bare", Content: "x"}, true); err == nil {
		t.Fatal("update without any description must fail")
	}
}

func TestStoredSkillMeta_ReadsBothTriggerShapes(t *testing.T) {
	dir := withSkillsDir(t)
	writeRichSkill(t, dir)
	desc, got := StoredSkillMeta("helm-chart")
	if desc != "Write Helm charts the house way" {
		t.Fatalf("description = %q", desc)
	}
	if want := []string{"helm chart", "chart values"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("list triggers = %v, want %v", got, want)
	}

	file := filepath.Join(dir, "scalar", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("---\nname: scalar\ndescription: d\ntriggers: deploy, ship it\n---\n\nbody\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, got := StoredSkillMeta("scalar"); !reflect.DeepEqual(got, []string{"deploy", "ship it"}) {
		t.Fatalf("scalar triggers = %v", got)
	}
	if desc, got := StoredSkillMeta("missing"); desc != "" || got != nil {
		t.Fatalf("missing skill meta = %q %v, want empty", desc, got)
	}
}

func TestSplitSkillMD(t *testing.T) {
	cases := []struct {
		name, in, fm, body string
		ok                 bool
	}{
		{"normal", "---\na: 1\n---\n\nbody\n", "a: 1", "\nbody\n", true},
		{"bom and blank lines", "\uFEFF\n---\na: 1\n---\nbody", "a: 1", "body", true},
		{"empty frontmatter", "---\n---\nbody", "", "body", true},
		{"no frontmatter", "# just markdown", "", "# just markdown", false},
		{"unclosed", "---\na: 1\nbody", "", "---\na: 1\nbody", false},
		{"dashes in the middle of a line", "---x\na: 1\n---\nbody", "", "---x\na: 1\n---\nbody", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fm, body, ok := splitSkillMD(tc.in)
			if fm != tc.fm || body != tc.body || ok != tc.ok {
				t.Fatalf("splitSkillMD = (%q, %q, %v), want (%q, %q, %v)", fm, body, ok, tc.fm, tc.body, tc.ok)
			}
		})
	}
}
