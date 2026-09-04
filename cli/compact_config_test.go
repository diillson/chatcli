/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"context"
	"math"
	"testing"

	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

func TestParseAutoCompactSetting(t *testing.T) {
	window := 200000
	cases := []struct {
		in   string
		want float64
		err  bool
	}{
		{"", 0, false}, {"default", 0, false}, {"reset", 0, false},
		{"60%", 0.6, false}, {"0.6", 0.6, false}, {"1", 1, false},
		{"150k", 0.75, false}, {"150000", 0.75, false}, {"0.5m", 1, false}, // clamped to the window
		{"0%", 0, true}, {"150%", 0, true}, {"abc", 0, true}, {"-3", 0, true},
	}
	for _, c := range cases {
		got, err := parseAutoCompactSetting(c.in, window)
		if (err != nil) != c.err {
			t.Fatalf("%q: err=%v want err=%v", c.in, err, c.err)
		}
		if !c.err && math.Abs(got-c.want) > 1e-9 {
			t.Fatalf("%q: got %v want %v", c.in, got, c.want)
		}
	}
	if _, err := parseAutoCompactSetting("150k", 0); err == nil {
		t.Fatal("token form needs a known window")
	}
}

func TestCompactConfig_SessionOverrideAndDefault(t *testing.T) {
	cli := &ChatCLI{Provider: "CLAUDEAI", Model: "claude-sonnet-5"}
	base := cli.compactConfig(cli.Provider, cli.Model)
	if base.BudgetRatio != 0.75 || base.SummarizerClient != nil {
		t.Fatalf("default cfg = %+v", base)
	}
	cli.handleAutoCompactCommand(context.Background(), "/autocompact 50%")
	if got := cli.compactConfig(cli.Provider, cli.Model).BudgetRatio; got != 0.5 {
		t.Fatalf("override not applied: %v", got)
	}
	cli.handleAutoCompactCommand(context.Background(), "/autocompact banana")
	if got := cli.compactConfig(cli.Provider, cli.Model).BudgetRatio; got != 0.5 {
		t.Fatalf("invalid value must keep the previous override: %v", got)
	}
	cli.handleAutoCompactCommand(context.Background(), "/autocompact default")
	if got := cli.compactConfig(cli.Provider, cli.Model).BudgetRatio; got != 0.75 {
		t.Fatalf("reset not applied: %v", got)
	}
	cli.handleAutoCompactCommand(context.Background(), "/autocompact") // status line, no panic
}

func TestCharBudget_ReservedCharsFloor(t *testing.T) {
	hc := NewHistoryCompactor(nil)
	cfg := CompactConfig{Provider: "CLAUDEAI", Model: "claude-sonnet-5", BudgetRatio: 0.5, CharsPerToken: 4}
	base := hc.CharBudget(cfg)
	cfg.ReservedChars = 1000
	if got := hc.CharBudget(cfg); got != base-1000 {
		t.Fatalf("reserved chars must be subtracted: base=%d got=%d", base, got)
	}
	cfg.ReservedChars = base * 2 // absurd tool catalog
	if got := hc.CharBudget(cfg); got != base/4 {
		t.Fatalf("budget must floor at a quarter: base=%d got=%d", base, got)
	}
}

// summarizerProbe records which client served the Level 2 summary.
type summarizerProbe struct {
	name  string
	calls *[]string
}

func (p *summarizerProbe) GetModelName() string { return p.name }
func (p *summarizerProbe) SendPrompt(_ context.Context, _ string, _ []models.Message, _ int) (string, error) {
	*p.calls = append(*p.calls, p.name)
	return "## Files Read\n- a.go\n## Current Task State\n- summarizing", nil
}

func TestStructuredSummarize_UsesConfiguredSummarizer(t *testing.T) {
	var calls []string
	session := &summarizerProbe{name: "session", calls: &calls}
	cheap := &summarizerProbe{name: "cheap", calls: &calls}
	hc := NewHistoryCompactor(zap.NewNop())

	history := []models.Message{{Role: "system", Content: "sys"}}
	for i := 0; i < 20; i++ {
		history = append(history, models.Message{Role: "user", Content: "u"}, models.Message{Role: "assistant", Content: "a"})
	}
	cfg := CompactConfig{Provider: "CLAUDEAI", Model: "claude-sonnet-5", BudgetRatio: 0.75, MinKeepRecent: 4, CharsPerToken: 4}

	if _, _, err := hc.structuredSummarize(context.Background(), history, session, cfg); err != nil {
		t.Fatalf("summarize: %v", err)
	}
	cfg.SummarizerClient = cheap
	if _, _, err := hc.structuredSummarize(context.Background(), history, session, cfg); err != nil {
		t.Fatalf("summarize with cheap model: %v", err)
	}
	if len(calls) != 2 || calls[0] != "session" || calls[1] != "cheap" {
		t.Fatalf("summarizer selection wrong: %v", calls)
	}
}
