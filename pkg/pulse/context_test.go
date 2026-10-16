/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package pulse

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParentTravelsInContext(t *testing.T) {
	ctx := context.Background()
	assert.Empty(t, ParentFrom(ctx))
	var none context.Context
	assert.Empty(t, ParentFrom(none), "a nil ctx is tolerated")
	assert.Equal(t, ctx, WithParent(ctx, ""), "an empty id leaves ctx as it is")

	outer := WithParent(ctx, "turn-1")
	inner := WithParent(outer, "pattern-2")
	assert.Equal(t, "turn-1", ParentFrom(outer))
	assert.Equal(t, "pattern-2", ParentFrom(inner), "the innermost parent wins")

	var nilSpan *Span
	assert.Empty(t, nilSpan.ID())
	assert.Equal(t, outer, nilSpan.Context(outer), "a nil span leaves ctx unchanged")
	nilSpan.Update(map[string]string{"k": "v"}) // nil-safe
}

func TestBeginCtxNestsUnderTheInnermostParent(t *testing.T) {
	b := Default()
	was := b.Enabled()
	b.SetEnabled(true)
	t.Cleanup(func() { b.SetEnabled(was) })
	ch, cancel := b.Subscribe(64)
	defer cancel()

	ctx, outer := BeginCtx(context.Background(), KindTurn, "chat", "session-fallback")
	require.NotNil(t, outer)
	assert.Equal(t, outer.ID(), ParentFrom(ctx))
	_, inner := BeginCtx(ctx, KindPattern, "fallback", "ignored")
	inner.Update(map[string]string{"attempt": "1", "": "skipped"})
	inner.End(StatusOK)
	inner.Update(map[string]string{"late": "x"}) // ignored after End
	outer.End(StatusOK)

	var got []Event
	for len(got) < 5 {
		select {
		case ev := <-ch:
			got = append(got, ev)
		case <-time.After(2 * time.Second):
			t.Fatalf("got %d events: %+v", len(got), got)
		}
	}
	assert.Equal(t, "session-fallback", got[0].Parent, "no parent in ctx: the fallback")
	assert.Equal(t, outer.ID(), got[1].Parent, "the inner span hangs from the outer one")
	assert.Equal(t, PhaseUpdate, got[2].Phase)
	assert.Equal(t, map[string]string{"attempt": "1"}, got[2].Attrs)
	assert.Equal(t, PhaseEnd, got[3].Phase)
	assert.Equal(t, outer.ID(), got[4].ID)
	select {
	case ev := <-ch:
		t.Fatalf("an update after End must not be emitted: %+v", ev)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestKindOf(t *testing.T) {
	cases := map[string]Kind{
		"turn-12":                   KindTurn,
		"pattern-3":                 KindPattern,
		"llm-9":                     KindLLM,
		"rpc-1":                     KindRPC,
		"run-abc-4":                 "",
		"session":                   "",
		"":                          "",
		"turn-x":                    "",
		"agent-3":                   "",
		"-5":                        "",
		"background-77":             KindBackground,
		"conn-2":                    KindConn,
		"tool-18446744073709551615": KindTool,
	}
	for id, want := range cases {
		assert.Equal(t, want, KindOf(id), id)
	}
}

func TestSummarizeNamesTheParentNode(t *testing.T) {
	t0 := time.Date(2026, 10, 16, 10, 0, 0, 0, time.UTC)
	events := []Event{
		{TS: t0, Kind: KindSession, Phase: PhaseStart, ID: "session", Name: "chatcli"},
		{TS: t0, Kind: KindTurn, Phase: PhaseStart, ID: "turn-1", Name: "moa"},
		{TS: t0, Kind: KindAgent, Phase: PhaseStart, ID: "run-a-1", Parent: "turn-1", Name: "panel"},
		{TS: t0, Kind: KindAgent, Phase: PhaseStart, ID: "run-a-2", Parent: "run-a-1", Name: "openai:gpt"},
		{TS: t0, Kind: KindLLM, Phase: PhaseStart, ID: "llm-1", Parent: "run-a-2", Name: "gpt"},
		{TS: t0, Kind: KindTool, Phase: PhaseStart, ID: "tool-1", Parent: "session", Name: "@read"},
		{TS: t0, Kind: KindConn, Phase: PhaseStart, ID: "conn-1", Parent: "unknown-9", Name: "api.example"},
	}
	byName := map[string]*Node{}
	for _, n := range Summarize(events).Nodes {
		byName[n.Name] = n
	}
	assert.Equal(t, "turn:moa", byName["panel"].ParentNode, "a run under a span names the span's hub")
	assert.Equal(t, "agent:panel", byName["openai:gpt"].ParentNode)
	assert.Equal(t, "agent:openai:gpt", byName["gpt"].ParentNode)
	assert.Empty(t, byName["@read"].ParentNode, "the session is not named")
	assert.Empty(t, byName["api.example"].ParentNode, "an unknown parent is not named")
}
