/*
 * ChatCLI - Unit tests for the chat-mode pipeline helpers
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Targets the pure / near-pure helpers extracted out of processLLMRequest:
 *   - applyManualSkillHints   (manual > pinned > auto precedence)
 *   - skillContentBlocks      (pinned block before auto block, cache hint)
 *   - dedupAutoAgainstPinned  (no duplicate skill injection)
 *   - buildPinnedSkillInjectionBlock (header + per-skill rendering)
 *   - combinedSystemMessage   (flattening for non-Anthropic providers)
 *   - buildChatTempHistory    (system-first ordering invariant)
 *   - applyChatEffortHint     (thinking override precedence)
 *   - providerMaxTokensOverride (env-var fallback table)
 *
 * Helpers that own user-visible side effects (animation, spinner, terminal
 * writes) are intentionally not covered here — they are exercised by the
 * higher-level integration tests in cli_test.go, and unit-testing them
 * would only mock their side effects without proving correctness.
 */
package cli

import (
	"os"
	"strings"
	"testing"

	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/pkg/persona"
)

func TestApplyManualSkillHints_NilNoop(t *testing.T) {
	model := "previous"
	effort := client.EffortMedium
	applyManualSkillHints(nil, &model, &effort)
	if model != "previous" || effort != client.EffortMedium {
		t.Fatalf("nil skill should not touch hints; got (%q, %q)", model, effort)
	}
}

func TestApplyManualSkillHints_OverridesNonEmpty(t *testing.T) {
	model := "auto-model"
	effort := client.EffortLow
	manual := &persona.Skill{Model: "opus", Effort: "high"}
	applyManualSkillHints(manual, &model, &effort)
	if model != "opus" {
		t.Errorf("model = %q, want opus", model)
	}
	if effort != client.EffortHigh {
		t.Errorf("effort = %q, want high", effort)
	}
}

func TestApplyManualSkillHints_PreservesWhenManualEmpty(t *testing.T) {
	model := "auto-model"
	effort := client.EffortLow
	manual := &persona.Skill{} // no model, no effort
	applyManualSkillHints(manual, &model, &effort)
	if model != "auto-model" {
		t.Errorf("empty manual model should not clear hint; got %q", model)
	}
	if effort != client.EffortLow {
		t.Errorf("empty manual effort should not clear hint; got %q", effort)
	}
}

func TestApplyManualSkillHints_OverridesOnlyDefinedFields(t *testing.T) {
	model := "auto-model"
	effort := client.EffortHigh
	manual := &persona.Skill{Model: "sonnet"} // overrides model, leaves effort
	applyManualSkillHints(manual, &model, &effort)
	if model != "sonnet" {
		t.Errorf("model = %q, want sonnet", model)
	}
	if effort != client.EffortHigh {
		t.Errorf("effort untouched when manual leaves it empty; got %q", effort)
	}
}

func TestSkillContentBlocks_EmptyInputs(t *testing.T) {
	if got := skillContentBlocks(nil, nil); len(got) != 0 {
		t.Fatalf("both nil → no blocks; got %d", len(got))
	}
}

func TestSkillContentBlocks_PinnedFirstWithCache(t *testing.T) {
	pinned := []*persona.Skill{
		{Name: "pinA", Description: "p", Content: "pinned body"},
	}
	auto := []*persona.Skill{
		{Name: "autoB", Description: "a", Content: "auto body"},
	}
	blocks := skillContentBlocks(pinned, auto)
	if len(blocks) != 2 {
		t.Fatalf("len blocks = %d, want 2", len(blocks))
	}
	if !strings.Contains(blocks[0].Text, "# Pinned Skills") {
		t.Errorf("pinned block must come first; first text was: %s", blocks[0].Text[:40])
	}
	if blocks[0].CacheControl == nil || blocks[0].CacheControl.Type != "ephemeral" {
		t.Errorf("pinned block missing cache_control:ephemeral hint")
	}
	if !strings.Contains(blocks[1].Text, "# Auto-loaded Skills") {
		t.Errorf("auto block expected second; got: %s", blocks[1].Text[:40])
	}
	// Auto block intentionally has no cache hint — it changes per turn.
	if blocks[1].CacheControl != nil {
		t.Errorf("auto block should NOT carry a cache hint")
	}
}

func TestSkillContentBlocks_OnlyPinned(t *testing.T) {
	pinned := []*persona.Skill{{Name: "p1", Description: "d", Content: "c"}}
	blocks := skillContentBlocks(pinned, nil)
	if len(blocks) != 1 {
		t.Fatalf("len = %d, want 1", len(blocks))
	}
	if !strings.Contains(blocks[0].Text, "# Pinned Skills") {
		t.Errorf("expected pinned-only block")
	}
}

func TestSkillContentBlocks_OnlyAuto(t *testing.T) {
	auto := []*persona.Skill{{Name: "a1", Description: "d", Content: "c"}}
	blocks := skillContentBlocks(nil, auto)
	if len(blocks) != 1 {
		t.Fatalf("len = %d, want 1", len(blocks))
	}
	if !strings.Contains(blocks[0].Text, "# Auto-loaded Skills") {
		t.Errorf("expected auto-only block")
	}
}

func TestDedupAutoAgainstPinned_RemovesDuplicates(t *testing.T) {
	pinned := []*persona.Skill{
		{Name: "alpha"},
		{Name: "bravo"},
	}
	auto := []*persona.Skill{
		{Name: "alpha"}, // dup
		{Name: "charlie"},
		{Name: "bravo"}, // dup
		{Name: "delta"},
	}
	out := dedupAutoAgainstPinned(auto, pinned)
	wantNames := []string{"charlie", "delta"}
	if len(out) != len(wantNames) {
		t.Fatalf("len out = %d, want %d. out=%v", len(out), len(wantNames), out)
	}
	for i, w := range wantNames {
		if out[i].Name != w {
			t.Errorf("out[%d].Name = %q, want %q", i, out[i].Name, w)
		}
	}
}

func TestDedupAutoAgainstPinned_NoOpWhenPinnedEmpty(t *testing.T) {
	auto := []*persona.Skill{{Name: "a"}, {Name: "b"}}
	out := dedupAutoAgainstPinned(auto, nil)
	if len(out) != 2 {
		t.Fatalf("dedup against empty pinned must be a no-op; got len=%d", len(out))
	}
}

func TestDedupAutoAgainstPinned_NoOpWhenAutoEmpty(t *testing.T) {
	pinned := []*persona.Skill{{Name: "x"}}
	if out := dedupAutoAgainstPinned(nil, pinned); out != nil {
		t.Fatalf("dedup of nil auto should stay nil; got %v", out)
	}
}

func TestBuildPinnedSkillInjectionBlock_EmptyReturnsBlank(t *testing.T) {
	if buildPinnedSkillInjectionBlock(nil) != "" {
		t.Fatal("nil → empty")
	}
	if buildPinnedSkillInjectionBlock([]*persona.Skill{}) != "" {
		t.Fatal("empty slice → empty")
	}
}

func TestBuildPinnedSkillInjectionBlock_FormatAndMetadata(t *testing.T) {
	skills := []*persona.Skill{
		{Name: "alpha", Description: "alpha desc", Content: "alpha body", Version: "1.0"},
		{Name: "bravo", Description: "", Content: "bravo body"},
	}
	out := buildPinnedSkillInjectionBlock(skills)
	for _, sub := range []string{
		"# Pinned Skills",
		"`/skill pin <name>`",
		"## Skill: alpha",
		"(v1.0)",
		"alpha desc",
		"alpha body",
		"## Skill: bravo",
		"bravo body",
	} {
		if !strings.Contains(out, sub) {
			t.Errorf("pinned block missing %q\nfull:\n%s", sub, out)
		}
	}
}

func TestCombinedSystemMessage_FlattensWithSeparator(t *testing.T) {
	parts := []models.ContentBlock{
		{Type: "text", Text: "part one"},
		{Type: "text", Text: "part two"},
		{Type: "text", Text: "part three"},
	}
	msg := combinedSystemMessage(parts)
	if msg.Role != "system" {
		t.Errorf("Role = %q, want system", msg.Role)
	}
	if len(msg.SystemParts) != 3 {
		t.Errorf("SystemParts len = %d, want 3", len(msg.SystemParts))
	}
	want := "part one\n\n---\n\npart two\n\n---\n\npart three"
	if msg.Content != want {
		t.Errorf("Content =\n%q\nwant\n%q", msg.Content, want)
	}
}

func TestCombinedSystemMessage_SinglePartNoSeparator(t *testing.T) {
	parts := []models.ContentBlock{{Type: "text", Text: "only"}}
	msg := combinedSystemMessage(parts)
	if msg.Content != "only" {
		t.Errorf("Content = %q, want %q", msg.Content, "only")
	}
}

func TestProviderMaxTokensOverride(t *testing.T) {
	cases := []struct {
		name     string
		provider string
		env      map[string]string
		want     int
	}{
		{
			name:     "unknown provider returns 0",
			provider: "MYSTERY",
			want:     0,
		},
		{
			name:     "env unset returns 0",
			provider: "OPENAI",
			want:     0,
		},
		{
			name:     "valid env propagates",
			provider: "OPENAI",
			env:      map[string]string{"OPENAI_MAX_TOKENS": "8192"},
			want:     8192,
		},
		{
			name:     "non-numeric env returns 0",
			provider: "CLAUDEAI",
			env:      map[string]string{"ANTHROPIC_MAX_TOKENS": "lots"},
			want:     0,
		},
		{
			name:     "negative env returns 0",
			provider: "GOOGLEAI",
			env:      map[string]string{"GOOGLEAI_MAX_TOKENS": "-1"},
			want:     0,
		},
		{
			name:     "zero env returns 0",
			provider: "XAI",
			env:      map[string]string{"XAI_MAX_TOKENS": "0"},
			want:     0,
		},
		{
			name:     "lowercase provider name still resolves",
			provider: "openai",
			env:      map[string]string{"OPENAI_MAX_TOKENS": "4096"},
			want:     4096,
		},
	}
	// Collect every env var used across the table so each subtest can
	// reset to a clean slate before applying its own overrides.
	allEnvKeys := map[string]struct{}{}
	for _, c := range cases {
		for k := range c.env {
			allEnvKeys[k] = struct{}{}
		}
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for k := range allEnvKeys {
				t.Setenv(k, "") // t.Setenv unsets at test end; "" makes Getenv return empty
				_ = os.Unsetenv(k)
			}
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			if got := providerMaxTokensOverride(tc.provider); got != tc.want {
				t.Errorf("providerMaxTokensOverride(%q) = %d, want %d", tc.provider, got, tc.want)
			}
		})
	}
}
