/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/diillson/chatcli/cli/compress"
	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

// countingSummarizer returns a canned summary and counts calls.
type countingSummarizer struct {
	calls   int
	summary string
	usage   *models.UsageInfo // template; a fresh copy is stored per call, like real clients
	last    *models.UsageInfo
}

func (c *countingSummarizer) GetModelName() string { return "counting" }
func (c *countingSummarizer) SendPrompt(context.Context, string, []models.Message, int) (string, error) {
	c.calls++
	if c.usage != nil {
		u := *c.usage
		c.last = &u
	}
	return c.summary, nil
}
func (c *countingSummarizer) LastUsage() *models.UsageInfo { return c.last }

func verbatimFixture() []models.Message {
	h := []models.Message{{Role: "system", Content: "charter"}}
	for i := 0; i < 3; i++ {
		h = append(h,
			models.Message{Role: "user", Content: strings.Repeat("question detail ", 50)},
			models.Message{Role: "assistant", Content: strings.Repeat("answer detail ", 50)},
		)
	}
	h = append(h, models.Message{Role: "user", Content: "RECALLED ORIGINAL " + strings.Repeat("x", 300), Meta: &models.MessageMeta{PreserveVerbatim: true}})
	h = append(h, models.Message{Role: "assistant", Content: strings.Repeat("more answer ", 50)})
	for i := 0; i < 2; i++ {
		h = append(h, models.Message{Role: "user", Content: "recent"})
	}
	return h
}

func hasVerbatim(h []models.Message) (int, bool) {
	for i, m := range h {
		if m.Meta != nil && m.Meta.PreserveVerbatim {
			return i, true
		}
	}
	return -1, false
}

func TestStructuredSummarize_KeepsPreserveVerbatimAfterSummary(t *testing.T) {
	hc := NewHistoryCompactor(zap.NewNop())
	cfg := CompactConfig{MinKeepRecent: 2}
	got, _, err := hc.structuredSummarize(context.Background(), verbatimFixture(), &countingSummarizer{summary: strings.Repeat("## Summary\n- point ", 8)}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	idx, ok := hasVerbatim(got)
	if !ok {
		t.Fatal("the PreserveVerbatim message must survive Level 2")
	}
	if got[idx-1].Meta == nil || !got[idx-1].Meta.IsSummary {
		t.Fatalf("verbatim message must follow the summary, got index %d of %d", idx, len(got))
	}
	if got[idx-1].Meta.SummaryOf != 7 {
		t.Fatalf("summary must cover the 7 compactable messages, got %d", got[idx-1].Meta.SummaryOf)
	}
}

func TestEmergencyTruncate_KeepsVerbatimAndArchives(t *testing.T) {
	layer := compress.NewLayer(compress.Config{Mode: compress.ModeLossyWithCCR, Store: compress.NewMemoryStore()})
	hc := NewHistoryCompactor(zap.NewNop())
	hc.SetCompressionLayer(layer)
	got := hc.emergencyTruncate(verbatimFixture(), CompactConfig{MinKeepRecent: 2})
	if _, ok := hasVerbatim(got); !ok {
		t.Fatal("Level 3 must not drop a PreserveVerbatim message")
	}
	if !strings.Contains(got[1].Content, "@recall") {
		t.Fatalf("Level 3 must archive what it drops to CCR: %q", got[1].Content)
	}
	if got[1].Meta.SummaryOf != 7 {
		t.Fatalf("dropped count excludes the verbatim message: %d", got[1].Meta.SummaryOf)
	}
}

func TestShrinkToBudget_SkipsVerbatim(t *testing.T) {
	h := []models.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: strings.Repeat("v", 5000), Meta: &models.MessageMeta{PreserveVerbatim: true}},
		{Role: "assistant", Content: strings.Repeat("a", 4000)},
	}
	got := shrinkToBudget(h, 6000, nil)
	if len(got[1].Content) != 5000 {
		t.Fatal("verbatim content must never be shrunk")
	}
	if len(got[2].Content) >= 4000 {
		t.Fatal("the other message must absorb the cut")
	}
}

func TestSummaryPassesGate(t *testing.T) {
	long := strings.Repeat("## Files\n- a.go\n", 10)
	cases := []struct {
		resp string
		seg  int
		want bool
	}{
		{"", 100, false},
		{"   ", 100, false},
		{"SUMMARY", 200, true},      // tiny segment: tiny floor
		{"SUMMARY", 100_000, false}, // huge segment: 7 chars is not a summary
		{"I'm sorry, I cannot help with that " + long, 100_000, false},
		{"Desculpe, não posso resumir " + long, 100_000, false},
		{long, 100_000, true},
	}
	for _, c := range cases {
		if got := summaryPassesGate(c.resp, c.seg); got != c.want {
			t.Fatalf("gate(%q, %d) = %v, want %v", c.resp[:min(len(c.resp), 20)], c.seg, got, c.want)
		}
	}
}

func TestStructuredSummarize_QualityGateRetriesThenFails(t *testing.T) {
	hc := NewHistoryCompactor(zap.NewNop())
	s := &countingSummarizer{summary: "I'm sorry, I cannot summarize this conversation for you today."}
	_, _, err := hc.structuredSummarize(context.Background(), summarizeFixtureHistory(2), s, CompactConfig{MinKeepRecent: 2})
	if err == nil || s.calls != 2 {
		t.Fatalf("a refusal must be retried once then rejected: err=%v calls=%d", err, s.calls)
	}
}

func TestCompact_BacksOffLevel2WhenItDoesNotConverge(t *testing.T) {
	hc := NewHistoryCompactor(zap.NewNop())
	// The "summary" is as big as the segment: Level 2 can never fit the budget.
	s := &countingSummarizer{summary: strings.Repeat("## Never converges\n- detail ", 400), usage: &models.UsageInfo{PromptTokens: 1000, CompletionTokens: 200}}
	// MaxPayloadBytes caps the budget at 0.7×6000 chars: below the summary.
	cfg := CompactConfig{Provider: "openai", Model: "gpt-5.6-terra", MinKeepRecent: 2, BudgetRatio: 0.5, CharsPerToken: 4, MaxPayloadBytes: 6000}
	history := summarizeFixtureHistory(2)
	for i := 0; i < 4; i++ {
		history = append(history, models.Message{Role: "user", Content: strings.Repeat("filler ", 400)}, models.Message{Role: "assistant", Content: strings.Repeat("reply ", 400)})
	}
	out, err := hc.Compact(context.Background(), history, s, cfg)
	if err != nil {
		t.Fatal(err)
	}
	rep := hc.LastReport()
	if rep.Level != 3 || s.calls != 1 || rep.SummaryUsage == nil || rep.SummaryProvider != "openai" {
		t.Fatalf("first run: level=%d calls=%d report=%+v", rep.Level, s.calls, rep)
	}
	// Next compactions skip the summarizer (back-off), then it comes back.
	for i := 1; i <= l2BackoffTurns; i++ {
		if _, err := hc.Compact(context.Background(), history, s, cfg); err != nil {
			t.Fatal(err)
		}
		if s.calls != 1 || !hc.LastReport().Level2SkippedByBO {
			t.Fatalf("run %d must skip Level 2: calls=%d report=%+v", i, s.calls, hc.LastReport())
		}
	}
	if _, err := hc.Compact(context.Background(), history, s, cfg); err != nil {
		t.Fatal(err)
	}
	if s.calls != 2 {
		t.Fatalf("after the back-off the summarizer must be tried again: calls=%d", s.calls)
	}
	_ = out
}

func TestRecordCompaction_CountsAndCosts(t *testing.T) {
	ct := NewCostTracker()
	ct.RecordCompaction(CompactReport{Level: 1})
	ct.RecordCompaction(CompactReport{Level: 3, SummaryProvider: "openai", SummaryModel: "gpt-5.6-terra", SummaryUsage: &models.UsageInfo{PromptTokens: 100_000, CompletionTokens: 2_000, TotalTokens: 102_000}})
	total, l3, cost := ct.CompactionStats()
	if total != 2 || l3 != 1 || cost <= 0 {
		t.Fatalf("stats = %d/%d/%f", total, l3, cost)
	}
	snap := ct.Snapshot()
	if snap.Compactions != 2 || snap.CompactionsLevel3 != 1 || snap.CompactionCostUSD != cost {
		t.Fatalf("snapshot = %+v", snap)
	}
	if snap.TotalCostUSD < cost {
		t.Fatalf("the summarizer request joins the session total: total=%f compaction=%f", snap.TotalCostUSD, cost)
	}
	var nilTracker *CostTracker
	nilTracker.RecordCompaction(CompactReport{Level: 2}) // must not panic
}

func TestArchiveDroppedMessages(t *testing.T) {
	layer := compress.NewLayer(compress.Config{Mode: compress.ModeLossyWithCCR, Store: compress.NewMemoryStore()})
	before := []models.Message{{Role: "system", Content: "sys"}, {Role: "user", Content: "keep"}, {Role: "assistant", Content: strings.Repeat("dropped ", 100)}, {Role: "user", Content: "keep"}}
	after := []models.Message{{Role: "system", Content: "sys"}, {Role: "user", Content: "keep"}}
	note := archiveDroppedMessages(layer, before, after)
	if !strings.Contains(note, "@recall") {
		t.Fatalf("dropped messages must be archived: %q", note)
	}
	if archiveDroppedMessages(layer, before, before) != "" || archiveDroppedMessages(nil, before, after) != "" {
		t.Fatal("nothing dropped / no layer must yield no note")
	}
}
