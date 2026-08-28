/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * handler_skills_test.go
 *
 * Server-side skill auto-activation: prompts served by the gRPC surface
 * (remote clients and the operator's analysis RPCs) must match the skill
 * catalog the same way interactive surfaces do.
 */
package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/pkg/persona"
	"go.uber.org/zap"
)

// writeServerTestSkill seeds one skill under a fake HOME so persona.NewManager
// (which resolves ~/.chatcli/skills) picks it up.
func writeServerTestSkill(t *testing.T, home, name, triggers, body string) {
	t.Helper()
	dir := filepath.Join(home, ".chatcli", "skills", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: " + name + "\ndescription: test skill for " + name + "\ntriggers: " + triggers + "\n---\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newSkillTestHandler(t *testing.T) *Handler {
	t.Helper()
	i18n.Init()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // windows UserHomeDir
	writeServerTestSkill(t, home, "helm-runbook", "helm, crashloopbackoff", "Always check values.yaml first.")

	h := NewHandler(nil, nil, zap.NewNop(), "OPENAI", "gpt-test")
	h.SetPersonaManager(persona.NewManager(zap.NewNop()))
	return h
}

func TestApplySkills_TriggerMatchInjectsBlock(t *testing.T) {
	h := newSkillTestHandler(t)
	prompt := "o pod está em CrashLoopBackOff depois do deploy com helm"
	out := h.applySkills(prompt)
	if !strings.Contains(out, "## Skill: helm-runbook") {
		t.Fatalf("expected skill block injected, got:\n%s", out)
	}
	if !strings.Contains(out, "Always check values.yaml first.") {
		t.Fatalf("expected skill body inlined, got:\n%s", out)
	}
	if !strings.Contains(out, prompt) {
		t.Fatalf("original prompt must be preserved, got:\n%s", out)
	}
}

func TestApplySkills_NoMatchIsPassthrough(t *testing.T) {
	h := newSkillTestHandler(t)
	prompt := "resuma este texto sem nenhum gatilho"
	if out := h.applySkills(prompt); out != prompt {
		t.Fatalf("no-match must be a passthrough, got:\n%s", out)
	}
}

func TestApplySkills_NilManagerIsPassthrough(t *testing.T) {
	i18n.Init()
	h := NewHandler(nil, nil, zap.NewNop(), "OPENAI", "gpt-test")
	prompt := "helm crashloopbackoff"
	if out := h.applySkills(prompt); out != prompt {
		t.Fatalf("nil manager must be a passthrough, got:\n%s", out)
	}
}

func TestEnrichPrompt_ComposesSkillsAndWatcherContext(t *testing.T) {
	h := newSkillTestHandler(t)
	h.SetWatcherContext(func() string { return "K8S CONTEXT" })
	out := h.enrichPrompt("problema de helm no cluster")
	if !strings.Contains(out, "K8S CONTEXT") {
		t.Fatalf("watcher context missing:\n%s", out)
	}
	if !strings.Contains(out, "## Skill: helm-runbook") {
		t.Fatalf("skill block missing:\n%s", out)
	}
}
