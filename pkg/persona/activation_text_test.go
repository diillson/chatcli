/*
 * ChatCLI - Persona System Tests (flat-prompt activation helpers)
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package persona

import (
	"strings"
	"testing"
)

func TestExtractPathTokens(t *testing.T) {
	got := ExtractPathTokens("olha o pkg/foo/bar_test.go e o main.go (e o deploy/chart/values.yaml).")
	want := []string{"pkg/foo/bar_test.go", "main.go", "deploy/chart/values.yaml"}
	if len(got) != len(want) {
		t.Fatalf("expected %v, got %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("expected %v, got %v", want, got)
		}
	}
}

func TestExtractPathTokens_EmptyAndProse(t *testing.T) {
	if got := ExtractPathTokens(""); got != nil {
		t.Fatalf("empty text must yield nil, got %v", got)
	}
	if got := ExtractPathTokens("nenhum caminho aqui, só prosa comum."); len(got) != 0 {
		t.Fatalf("prose must yield nothing, got %v", got)
	}
}

func TestExtractPathTokens_Dedup(t *testing.T) {
	got := ExtractPathTokens("main.go e de novo main.go")
	if len(got) != 1 || got[0] != "main.go" {
		t.Fatalf("expected single deduped token, got %v", got)
	}
}

func TestBuildActivationPromptBlock(t *testing.T) {
	skills := []*Skill{
		{Name: "helm-deploy", Version: "1.0", Description: "How we deploy Helm charts.", Content: "Step one. Step two."},
		{Name: "empty-body", Description: "Header-only skill."},
	}
	block := BuildActivationPromptBlock(skills, DefaultActivationBudget)
	for _, want := range []string{"# Auto-loaded Skills", "## Skill: helm-deploy (v1.0)", "Step one. Step two.", "## Skill: empty-body"} {
		if !strings.Contains(block, want) {
			t.Fatalf("block missing %q:\n%s", want, block)
		}
	}
	if BuildActivationPromptBlock(nil, DefaultActivationBudget) != "" {
		t.Fatal("no skills must render empty")
	}
}

func TestBuildActivationPromptBlock_BudgetOverflowDegradesToPointer(t *testing.T) {
	big := strings.Repeat("x", 100)
	skills := []*Skill{
		{Name: "first", Content: big, Path: "/tmp/first/SKILL.md"},
		{Name: "second", Content: big, Path: "/tmp/second/SKILL.md"},
	}
	block := BuildActivationPromptBlock(skills, 150)
	if !strings.Contains(block, big) {
		t.Fatal("first skill body should inline within budget")
	}
	if strings.Count(block, big) != 1 {
		t.Fatal("second skill body must NOT inline past the budget")
	}
	if !strings.Contains(block, "/tmp/second/SKILL.md") {
		t.Fatal("overflowing skill must keep a source pointer")
	}
}
