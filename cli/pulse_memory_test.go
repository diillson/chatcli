/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/diillson/chatcli/cli/ctxmgr"
	"github.com/diillson/chatcli/cli/workspace"
	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/pkg/pulse"
	"github.com/diillson/chatcli/pkg/pulse/pulsetest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// The bus is process-global: none of these tests run in parallel.

// atoiAttr reads a numeric attribute, failing the test when it is absent.
func atoiAttr(t *testing.T, ev pulse.Event, key string) int {
	t.Helper()
	v, ok := ev.Attrs[key]
	require.Truef(t, ok, "attribute %q missing from %+v", key, ev)
	n, err := strconv.Atoi(v)
	require.NoError(t, err)
	return n
}

// The session-start memory card is one span with the counts it rendered.
func TestPulseMemoryBootstrapReportsTheSnapshot(t *testing.T) {
	rec := pulsetest.Record(t)
	cli := newBootstrapCLI(t)
	withTempBoard(t)
	require.True(t, cli.memoryStore.Manager().Facts.AddFact("Bedrock max tokens come from the family catalog", "architecture", nil))

	_, agentCard := cli.memoryBootstrapCards()
	require.NotEmpty(t, agentCard)

	ev := rec.WaitFor(t, "memory bootstrap end", pulsetest.Is(pulse.KindBackground, pulseMemoryBootstrapNode, pulse.PhaseEnd))
	assert.Equal(t, pulse.StatusOK, ev.Status)
	assert.Equal(t, "card built", ev.Attrs["state"])
	assert.Equal(t, 1, atoiAttr(t, ev, "facts"))
	assert.Equal(t, 1, atoiAttr(t, ev, "surfaces"))
	assert.Equal(t, len(agentCard), atoiAttr(t, ev, "chars"))
}

// Auto-recall hangs from the turn in its context and reports what it found,
// including when it found nothing.
func TestPulseAutoRecallReportsHitsUnderItsParent(t *testing.T) {
	t.Setenv("CHATCLI_MEMORY_AUTORECALL", "")
	rec := pulsetest.Record(t)
	store := workspace.NewMemoryStore(t.TempDir(), zap.NewNop())
	cli := &ChatCLI{memoryStore: store, logger: zap.NewNop()}
	require.True(t, store.Manager().Facts.AddFact("The deploy pipeline uses helm charts from the infra repo", "project", []string{"deploy", "helm"}))

	ctx := pulse.WithParent(context.Background(), "turn-42")
	block := cli.memoryAutoRecallBlockCtx(ctx, []string{"deploy", "helm", "pipeline"}, "how does the deploy pipeline work")
	require.Contains(t, block, "helm charts")
	_ = cli.memoryAutoRecallBlockCtx(ctx, []string{"coffee", "office"}, "the coffee machine is broken")

	isEnd := pulsetest.Is(pulse.KindBackground, pulseAutoRecallNode, pulse.PhaseEnd)
	rec.WaitFor(t, "two auto-recall ends", func(ev pulse.Event) bool { return isEnd(ev) && len(rec.Match(isEnd)) >= 2 })
	ends := rec.Match(isEnd)
	hit, miss := ends[0], ends[1]

	assert.Equal(t, "turn-42", hit.Parent)
	assert.Equal(t, pulse.StatusOK, hit.Status)
	assert.Equal(t, "injected", hit.Attrs["state"])
	assert.Equal(t, 1, atoiAttr(t, hit, "facts"))
	assert.Equal(t, 1, atoiAttr(t, hit, "hits"))
	assert.Equal(t, 3, atoiAttr(t, hit, "hints"))
	assert.Equal(t, len(block), atoiAttr(t, hit, "chars"))
	assert.Equal(t, "false", hit.Attrs["semantic"])
	assert.Equal(t, "false", hit.Attrs["external"])

	assert.Equal(t, "nothing relevant", miss.Attrs["state"])
	assert.Equal(t, 0, atoiAttr(t, miss, "facts"))
	assert.Equal(t, 0, atoiAttr(t, miss, "chars"))

	for _, ev := range rec.Events() {
		for _, v := range ev.Attrs {
			assert.NotContains(t, v, "helm", "recalled content must never reach the bus")
		}
	}
}

// Saved-session recall reports how many sessions it pointed at.
func TestPulseSessionRecallReportsTheSessions(t *testing.T) {
	rec := pulsetest.Record(t)
	c := newSessionRecallCLI(t)

	block := c.sessionAutoRecallBlock(nil, "o que a gente fez na sessão anterior sobre o delegate?")
	require.NotEmpty(t, block)
	_ = c.sessionAutoRecallBlock([]string{"kubernetes", "ingress"}, "novo assunto")

	isEnd := pulsetest.Is(pulse.KindBackground, pulseSessionRecallNode, pulse.PhaseEnd)
	rec.WaitFor(t, "two session-recall ends", func(ev pulse.Event) bool { return isEnd(ev) && len(rec.Match(isEnd)) >= 2 })
	ends := rec.Match(isEnd)

	assert.Equal(t, "injected", ends[0].Attrs["state"])
	assert.Equal(t, "true", ends[0].Attrs["referential"])
	assert.Equal(t, 1, atoiAttr(t, ends[0], "sessions"))
	assert.Equal(t, len(block), atoiAttr(t, ends[0], "chars"))
	assert.Equal(t, "nothing relevant", ends[1].Attrs["state"])
	assert.Equal(t, "false", ends[1].Attrs["referential"])
}

// The /context attach auto-RAG decision is a point naming the guard.
func TestPulseAutoRagDecisionNamesTheGuard(t *testing.T) {
	rec := pulsetest.Record(t)
	h := autoRagFixture(t)
	t.Setenv(attachAutoRagEnvVar, "")

	assert.False(t, h.shouldAutoRag(&ctxmgr.FileContext{TotalSize: attachAutoRagMinBytes}, attachFlags{}))
	assert.False(t, h.shouldAutoRag(&ctxmgr.FileContext{TotalSize: 10}, attachFlags{full: true}))

	isPoint := pulsetest.Is(pulse.KindBackground, pulseAutoRagNode, pulse.PhasePoint)
	rec.WaitFor(t, "two auto-rag points", func(ev pulse.Event) bool { return isPoint(ev) && len(rec.Match(isPoint)) >= 2 })
	pts := rec.Match(isPoint)
	assert.Equal(t, "no embeddings", pts[0].Attrs["reason"])
	assert.Equal(t, "false", pts[0].Attrs["upgraded"])
	assert.Equal(t, attachAutoRagMinBytes, atoiAttr(t, pts[0], "bytes"))
	assert.Equal(t, "explicit choice", pts[1].Attrs["reason"])
}

// Knowledge search, document reads and per-turn retrieval each report
// themselves; the per-turn retrieval hangs from the turn in its context.
func TestPulseKnowledgeRetrievalSpans(t *testing.T) {
	rec := pulsetest.Record(t)
	cli := newKnowledgeTestCLI(t)
	a := &knowledgePluginAdapter{cli: cli}

	_, err := a.Search("homebrew install", "", 500)
	require.NoError(t, err)
	search := rec.WaitFor(t, "knowledge search end", pulsetest.Is(pulse.KindBackground, "knowledge:search", pulse.PhaseEnd))
	assert.Equal(t, pulse.StatusOK, search.Status)
	assert.Equal(t, 1, atoiAttr(t, search, "kbs"))
	assert.Equal(t, knowledgeSearchMaxTopK, atoiAttr(t, search, "top_k"))
	assert.Positive(t, atoiAttr(t, search, "hits"))
	assert.Equal(t, 0, atoiAttr(t, search, "failed"))

	doc, err := a.Get("guide/install.md", "", 0)
	require.NoError(t, err)
	get := rec.WaitFor(t, "knowledge get end", pulsetest.Is(pulse.KindBackground, pulseKnowledgeGetNode, pulse.PhaseEnd))
	assert.Equal(t, pulse.StatusOK, get.Status)
	assert.Positive(t, atoiAttr(t, get, "page_chars"))
	assert.Less(t, atoiAttr(t, get, "page_chars"), len(doc))
	assert.Equal(t, "false", get.Attrs["more"])

	_, err = a.Get("guide/missing.md", "", 0)
	require.Error(t, err)
	rec.WaitFor(t, "failed knowledge get", func(ev pulse.Event) bool {
		return pulsetest.Is(pulse.KindBackground, pulseKnowledgeGetNode, pulse.PhaseEnd)(ev) && ev.Status == pulse.StatusError
	})

	ctx := pulse.WithParent(context.Background(), "turn-9")
	msgs, err := cli.contextHandler.GetManager().BuildRetrievedContextMessages(ctx, "default", "homebrew install")
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	ret := rec.WaitFor(t, "context retrieve end", pulsetest.Is(pulse.KindBackground, "context:retrieve", pulse.PhaseEnd))
	assert.Equal(t, "turn-9", ret.Parent)
	assert.Equal(t, 1, atoiAttr(t, ret, "contexts"))
	assert.Equal(t, 1, atoiAttr(t, ret, "blocks"))
	assert.Positive(t, atoiAttr(t, ret, "passages"))
	assert.Equal(t, len(msgs[0].Content), atoiAttr(t, ret, "chars"))
	assert.Equal(t, "false", ret.Attrs["budget_exhausted"])

	for _, ev := range rec.Events() {
		for _, v := range ev.Attrs {
			assert.NotContains(t, v, "homebrew", "queries must never reach the bus")
			assert.NotContains(t, v, "install.md", "sources must never reach the bus")
		}
	}
}

// The compaction span carries the history's shape before and after, and the
// summarizer's spend, when the compactor ran inside it — and nothing stale
// when it did not.
func TestPulseCompactionCarriesTheHistoryShape(t *testing.T) {
	rec := pulsetest.Record(t)
	hc := NewHistoryCompactor(zap.NewNop())
	c := &ChatCLI{logger: zap.NewNop(), historyCompactor: hc}

	s := &countingSummarizer{summary: strings.Repeat("## Never converges\n- detail ", 400), usage: &models.UsageInfo{PromptTokens: 1000, CompletionTokens: 200}}
	cfg := CompactConfig{Provider: "openai", Model: "gpt-5.6-terra", MinKeepRecent: 2, BudgetRatio: 0.5, CharsPerToken: 4, MaxPayloadBytes: 6000}
	history := summarizeFixtureHistory(2)
	for i := 0; i < 4; i++ {
		history = append(history, models.Message{Role: "user", Content: strings.Repeat("filler ", 400)}, models.Message{Role: "assistant", Content: strings.Repeat("reply ", 400)})
	}

	ctx := pulse.WithParent(context.Background(), "turn-3")
	c.beforeCompaction(ctx, compactTriggerAuto)
	out, err := hc.Compact(ctx, history, s, cfg)
	require.NoError(t, err)
	c.noteCompactionApplied(ctx, compactTriggerAuto)

	c.beforeCompaction(ctx, compactTriggerManual) // no Compact inside this one
	c.compactionSkipped(ctx, compactTriggerManual)

	isEnd := pulsetest.Is(pulse.KindBackground, pulseCompactionNode, pulse.PhaseEnd)
	rec.WaitFor(t, "two compaction ends", func(ev pulse.Event) bool { return isEnd(ev) && len(rec.Match(isEnd)) >= 2 })
	ends := rec.Match(isEnd)
	ev := ends[0]

	assert.Equal(t, "turn-3", ev.Parent)
	assert.Equal(t, compactOutcomeApplied, ev.Attrs["state"])
	assert.Equal(t, 3, atoiAttr(t, ev, "level"))
	assert.Equal(t, len(history), atoiAttr(t, ev, "msgs_before"))
	assert.Equal(t, len(out), atoiAttr(t, ev, "msgs_after"))
	assert.Equal(t, totalChars(history), atoiAttr(t, ev, "chars_before"))
	assert.Equal(t, totalChars(out), atoiAttr(t, ev, "chars_after"))
	assert.Equal(t, "openai", ev.Attrs["provider"])
	assert.Equal(t, "gpt-5.6-terra", ev.Attrs["model"])
	assert.Equal(t, 1000, atoiAttr(t, ev, "summary_tokens_in"))
	assert.Equal(t, 200, atoiAttr(t, ev, "summary_tokens_out"))

	skipped := ends[1]
	assert.Equal(t, compactOutcomeSkipped, skipped.Attrs["state"])
	assert.NotContains(t, skipped.Attrs, "level", "a compaction that did not run must not report the previous one's shape")
}
