/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

func TestSummarizerUsage_OnlyFromItsOwnCall(t *testing.T) {
	hc := NewHistoryCompactor(zap.NewNop())
	// The client carries sticky usage from a previous interactive turn.
	stale := &models.UsageInfo{PromptTokens: 9999, CompletionTokens: 9, IsReal: true}
	s := &countingSummarizer{summary: strings.Repeat("## Summary\n- point ", 8), usage: stale}
	// External engine serves the summary: the model is not called, no usage.
	cfg := CompactConfig{MinKeepRecent: 2, ExternalSummarizer: engineFunc(func(context.Context, string, int, string) (string, error) {
		return strings.Repeat("## External\n- point ", 8), nil
	})}
	_, usage, err := hc.structuredSummarize(context.Background(), summarizeFixtureHistory(2), s, cfg)
	if err != nil || usage != nil || s.calls != 0 {
		t.Fatalf("no model call → no usage: usage=%v calls=%d err=%v", usage, s.calls, err)
	}
	// Too-short history: early return, no usage.
	if _, usage, _ := hc.structuredSummarize(context.Background(), summarizeFixtureHistory(2)[:3], s, CompactConfig{MinKeepRecent: 2}); usage != nil {
		t.Fatal("early return must not book the stale usage")
	}
	// Real call: the client stores a fresh pointer → attributed.
	fresh := &models.UsageInfo{PromptTokens: 100, CompletionTokens: 20, IsReal: true}
	s2 := &usageSwitchingSummarizer{summary: strings.Repeat("## Summary\n- point ", 8), before: stale, after: fresh}
	_, usage, err = hc.structuredSummarize(context.Background(), summarizeFixtureHistory(2), s2, CompactConfig{MinKeepRecent: 2})
	if err != nil || usage != fresh {
		t.Fatalf("the summarizer's own call must be attributed: %v %v", usage, err)
	}
	if sumUsage(nil, fresh) != fresh || sumUsage(fresh, nil) != fresh || sumUsage(fresh, fresh).PromptTokens != 200 {
		t.Fatal("sumUsage")
	}
}

// usageSwitchingSummarizer reports `before` until its first call, then `after`.
type usageSwitchingSummarizer struct {
	summary       string
	before, after *models.UsageInfo
	called        bool
}

func (u *usageSwitchingSummarizer) GetModelName() string { return "switching" }
func (u *usageSwitchingSummarizer) SendPrompt(context.Context, string, []models.Message, int) (string, error) {
	u.called = true
	return u.summary, nil
}
func (u *usageSwitchingSummarizer) LastUsage() *models.UsageInfo {
	if u.called {
		return u.after
	}
	return u.before
}

func TestGeminiReasoningTokens_BilledAdditively(t *testing.T) {
	usage := &models.UsageInfo{PromptTokens: 1000, CompletionTokens: 100, ReasoningTokens: 900, TotalTokens: 2000, IsReal: true}
	gemini := estimateTurnCostUSD("googleai", "gemini-3.1-pro", usage)
	base := estimateTurnCostUSD("googleai", "gemini-3.1-pro", &models.UsageInfo{PromptTokens: 1000, CompletionTokens: 100, TotalTokens: 1100, IsReal: true})
	if !(gemini > base) {
		t.Fatalf("thinking tokens must add to Gemini output cost: %f vs %f", gemini, base)
	}
	openai := estimateTurnCostUSD("openai", "gpt-5.6-terra", usage)
	openaiBase := estimateTurnCostUSD("openai", "gpt-5.6-terra", &models.UsageInfo{PromptTokens: 1000, CompletionTokens: 100, TotalTokens: 1100, IsReal: true})
	if openai != openaiBase {
		t.Fatal("OpenAI reasoning tokens are already inside completion tokens")
	}
}

func TestStreamingWithoutUsage_BooksAnEstimate(t *testing.T) {
	ct := NewCostTracker()
	ct.RecordRealUsage("openai", "gpt-5.6-terra", models.EstimateFromChars(40_000, 4_000))
	if ct.TotalTokens() == 0 || ct.TotalCost() <= 0 {
		t.Fatalf("an estimate must be counted: tokens=%d cost=%f", ct.TotalTokens(), ct.TotalCost())
	}
}

func TestMemoryWorker_BudgetGateAndRequeueCap(t *testing.T) {
	t.Setenv("CHATCLI_SESSION_BUDGET_USD", "0.01")
	t.Setenv("CHATCLI_BUDGET_HARD_STOP", "true")
	active := &scriptedClient{name: "claude", response: "NOTHING_NEW"}
	mw := newResilienceWorker(t, active)
	mw.cli.costTracker = NewCostTracker()
	mw.cli.costTracker.RecordRealUsage("openai", "gpt-5.6-terra", &models.UsageInfo{PromptTokens: 5_000_000, CompletionTokens: 10, TotalTokens: 5_000_010, IsReal: true})
	if _, err := mw.callExtraction(context.Background(), "p", nil); err == nil {
		t.Fatal("the budget hard stop must gate background extraction")
	}
	t.Setenv("CHATCLI_BUDGET_HARD_STOP", "false")
	mw.cli.costTracker.ReloadBudget()
	// A segment whose provider keeps failing is retried once, then dropped.
	atomic.StoreInt32(&active.failN, 99)
	segment := []models.Message{{Role: "user", Content: "fato importante"}}
	if _, err := mw.persistPending(segment); err != nil {
		t.Fatal(err)
	}
	mw.drainPending(context.Background())
	if got := len(mw.pendingFiles()); got != 1 {
		t.Fatalf("first failure keeps the segment: %d", got)
	}
	mw.drainPending(context.Background())
	if got := len(mw.pendingFiles()); got != 0 {
		t.Fatalf("second failure drops it: %d", got)
	}
	// An unparseable reply is a failure (queued), not "nothing new".
	if looksLikeExtraction("I cannot help with that") || !looksLikeExtraction("## DAILY\n- x") {
		t.Fatal("looksLikeExtraction")
	}
	_ = errors.New
	_ = os.Remove
	_ = filepath.Join
}
