/*
 * ChatCLI - Persona System
 * pkg/persona/builtin_seed_integration_test.go
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package persona

import (
	"path/filepath"
	"testing"

	"github.com/diillson/chatcli/pkg/persona/builtin"
	"go.uber.org/zap"
)

// End-to-end: the embedded essential skills, once seeded, are discovered and
// parsed by the real Loader — frontmatter (name/triggers/allowed-tools) intact.
// This guards the whole shipping path: embed → Seed → Loader.
func TestBuiltinSkillsLoadThroughLoader(t *testing.T) {
	root := t.TempDir()
	skillsDir := filepath.Join(root, ".agent", "skills")
	if _, err := builtin.Seed(skillsDir, zap.NewNop()); err != nil {
		t.Fatalf("seed: %v", err)
	}

	l := NewLoader(zap.NewNop())
	l.SetProjectDir(root) // loader scans <projectDir>/.agent/skills

	skills, err := l.ListSkills()
	if err != nil {
		t.Fatalf("ListSkills: %v", err)
	}

	byName := map[string]*Skill{}
	for _, s := range skills {
		byName[s.Name] = s
	}

	// send-message must be present with its trigger keywords and @send tool.
	sm, ok := byName["send-message"]
	if !ok {
		t.Fatalf("send-message not loaded; got %v", keysOf(byName))
	}
	if len(sm.Triggers) == 0 {
		t.Error("send-message has no triggers parsed")
	}
	if !hasTool(sm.Tools, "@send") {
		t.Errorf("send-message allowed-tools missing @send: %v", sm.Tools)
	}

	for _, want := range []string{"email", "calendar", "reminders"} {
		if _, ok := byName[want]; !ok {
			t.Errorf("essential skill %q not loaded", want)
		}
	}
}

func keysOf(m map[string]*Skill) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func hasTool(tools []string, want string) bool {
	for _, tl := range tools {
		if tl == want {
			return true
		}
	}
	return false
}
