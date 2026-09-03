/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"math"
	"strings"
	"testing"

	"github.com/diillson/chatcli/cli/compress"
	"github.com/diillson/chatcli/models"
)

func TestTokenCalibrator_LearnsAndClamps(t *testing.T) {
	c := newTokenCalibrator()
	if r, n := c.CharsPerToken("CLAUDEAI", "claude-sonnet-5"); r != defaultCharsPerToken || n != 0 {
		t.Fatalf("fresh calibrator = %v/%d", r, n)
	}
	// 12000 chars for 4000 tokens → 3.0
	c.Observe("CLAUDEAI", "claude-sonnet-5", 12000, 4000)
	if r, n := c.CharsPerToken("claudeai", "Claude-Sonnet-5"); math.Abs(r-3.0) > 1e-9 || n != 1 {
		t.Fatalf("first sample = %v/%d (key must be case-insensitive)", r, n)
	}
	// Second sample at 5.0 moves the EMA toward it without jumping.
	c.Observe("CLAUDEAI", "claude-sonnet-5", 20000, 4000)
	r, _ := c.CharsPerToken("CLAUDEAI", "claude-sonnet-5")
	if r <= 3.0 || r >= 5.0 {
		t.Fatalf("EMA should sit between samples, got %v", r)
	}
	// Garbage samples are ignored: tiny prompts, absurd ratios.
	c.Observe("CLAUDEAI", "claude-sonnet-5", 100, 10)
	c.Observe("CLAUDEAI", "claude-sonnet-5", 1_000_000, 1000)
	if r2, n := c.CharsPerToken("CLAUDEAI", "claude-sonnet-5"); r2 != r || n != 2 {
		t.Fatalf("out-of-band samples must be ignored: %v/%d", r2, n)
	}
	if got := c.EstimateTokens("CLAUDEAI", "claude-sonnet-5", 0); got != 0 {
		t.Fatalf("zero chars → 0 tokens, got %d", got)
	}
	// Other models keep the default.
	if r3, n := c.CharsPerToken("OPENAI", "gpt-5.6"); r3 != defaultCharsPerToken || n != 0 {
		t.Fatalf("unrelated model learned something: %v/%d", r3, n)
	}
}

func TestCompactBudget_UsesLearnedRatio(t *testing.T) {
	hc := NewHistoryCompactor(nil)
	cfg := CompactConfig{Provider: "CLAUDEAI", Model: "claude-sonnet-5", BudgetRatio: 0.5, CharsPerToken: 4}
	base := hc.CharBudget(cfg)
	cfg.CharsPerTokenPrecise = 2.0
	if got := hc.CharBudget(cfg); got != base/2 {
		t.Fatalf("precise ratio 2.0 must halve the char budget: base=%d got=%d", base, got)
	}
}

func TestDefaultCompactConfig_PicksUpCalibration(t *testing.T) {
	prev := globalTokenCalibrator
	globalTokenCalibrator = newTokenCalibrator()
	t.Cleanup(func() { globalTokenCalibrator = prev })

	if cfg := DefaultCompactConfig("XAI", "grok-4.6"); cfg.CharsPerTokenPrecise != 0 {
		t.Fatalf("no samples → integer default only, got %v", cfg.CharsPerTokenPrecise)
	}
	globalTokenCalibrator.Observe("XAI", "grok-4.6", 9000, 3000)
	if cfg := DefaultCompactConfig("XAI", "grok-4.6"); math.Abs(cfg.CharsPerTokenPrecise-3.0) > 1e-9 {
		t.Fatalf("learned ratio not applied: %v", cfg.CharsPerTokenPrecise)
	}
}

func TestSummarizeHistory_RolesSummariesAndMarkers(t *testing.T) {
	h := []models.Message{
		{Role: "system", Content: strings.Repeat("s", 100)},
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi there"},
		{Role: "tool", Content: "out " + compress.FormatMarker(compress.KeyFor("archived original")), ToolCallID: "t1"},
		{Role: "user", Content: "[STRUCTURED SUMMARY]", Meta: &models.MessageMeta{IsSummary: true, SummaryOf: 5}},
	}
	st := summarizeHistory(h)
	if st.Messages != 5 || st.Summaries != 1 || st.CCRMarkers != 1 {
		t.Fatalf("stats = %+v", st)
	}
	if len(st.Roles) != 4 || st.Roles[0].Role != "system" {
		t.Fatalf("roles must be sorted by chars desc, got %+v", st.Roles)
	}
}

func TestChatAssembly_AddRecordsSections(t *testing.T) {
	var a chatSystemAssembly
	a.add("mode", models.ContentBlock{Type: "text", Text: "banner", CacheControl: &models.CacheControl{Type: "ephemeral"}})
	a.add("attached", models.ContentBlock{Type: "text", Text: "ctx-a"}, models.ContentBlock{Type: "text", Text: "ctx-b"})
	a.add("dynamic")
	if len(a.parts) != 3 || len(a.sections) != 3 {
		t.Fatalf("parts=%d sections=%d", len(a.parts), len(a.sections))
	}
	if !a.sections[0].Cached || a.sections[1].Cached || a.sections[1].Name != "attached" {
		t.Fatalf("sections = %+v", a.sections)
	}
	var store promptBreakdownStore
	store.record("chat", append(a.sections, promptSection{Name: "empty", Chars: 0}))
	b := store.latest()
	if b == nil || b.Mode != "chat" || len(b.Sections) != 3 || b.TotalChars() != len("banner")+len("ctx-a")+len("ctx-b") || b.CachedChars() != len("banner") {
		t.Fatalf("breakdown = %+v", b)
	}
}

func TestBuildContextStatusReport_ProjectsWindowUse(t *testing.T) {
	prev := globalTokenCalibrator
	globalTokenCalibrator = newTokenCalibrator()
	t.Cleanup(func() { globalTokenCalibrator = prev })
	globalTokenCalibrator.Observe("CLAUDEAI", "claude-sonnet-5", 8000, 2000) // 4.0

	cli := &ChatCLI{Provider: "CLAUDEAI", Model: "claude-sonnet-5"}
	cli.promptBreakdowns.record("coder", []promptSection{
		{Name: "core", Chars: 4000, Cached: true},
		{Name: "dynamic", Chars: 400},
	})
	cli.history = []models.Message{
		{Role: "system", Content: strings.Repeat("s", 4400)},
		{Role: "user", Content: strings.Repeat("u", 2000)},
		{Role: "assistant", Content: strings.Repeat("a", 2000)},
	}
	r := cli.buildContextStatusReport()
	if r.PromptTokens != 1100 || r.HistoryTokens != 1000 || r.TotalTokens != 2100 {
		t.Fatalf("tokens = prompt %d history %d total %d", r.PromptTokens, r.HistoryTokens, r.TotalTokens)
	}
	if r.Window <= 0 || r.ProjectedPct <= 0 || r.CompactAtPct != 75 {
		t.Fatalf("window/projection = %+v", r)
	}
	if r.CompactBudgetTokens != int(float64(r.Window)*0.75) {
		t.Fatalf("compact budget = %d for window %d", r.CompactBudgetTokens, r.Window)
	}
	// Rendering must not panic on a populated report or on an empty session.
	cli.showContextStatus()
	(&ChatCLI{Provider: "OPENAI", Model: "gpt-5.6"}).showContextStatus()
}
