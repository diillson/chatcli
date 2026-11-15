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

	"github.com/diillson/chatcli/i18n"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const handoffReply = "The parser tests need a run.\n\n<coder_handoff>run the parser tests</coder_handoff>"

// systemText is everything the model was given as system prompt on the
// fake's last call.
func systemText(fake *rpcChatFakeClient) string {
	var b strings.Builder
	for _, m := range fake.lastHist {
		if m.Role != "system" {
			continue
		}
		b.WriteString(m.Content)
		for _, p := range m.SystemParts {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

// A surface that can confirm gets the instruction to the model and the
// proposed task back, with the proposal line out of the reply and of the
// stored history.
func TestRunChatTurnRPC_HandoffSurfaceGetsTheTask(t *testing.T) {
	t.Setenv(coderHandoffEnv, "")
	fake := &rpcChatFakeClient{reply: handoffReply}
	c := newRPCChatCLI(t, fake)

	turn, err := c.RunChatTurnRPC(context.Background(), "web", "do the parser tests pass?", nil, RPCChatOpts{Handoff: true})
	require.NoError(t, err)
	assert.Equal(t, "run the parser tests", turn.HandoffTask)
	assert.Equal(t, "The parser tests need a run.", turn.Reply)
	assert.Contains(t, systemText(fake), "<coder_handoff>", "the model is asked to propose")
	for _, m := range turn.History {
		assert.NotContains(t, m.Content, "coder_handoff", "the proposal line is never stored")
	}
	assert.False(t, c.rpcChatTurn, "the turn's handoff state is restored")
	assert.False(t, c.coderHandoffActive(), "and does not leak into the process")
}

// A surface that cannot confirm keeps the previous contract: the model is
// not asked, and a stray proposal is dropped.
func TestRunChatTurnRPC_NoHandoffSurfaceDropsTheProposal(t *testing.T) {
	t.Setenv(coderHandoffEnv, "")
	fake := &rpcChatFakeClient{reply: handoffReply}
	c := newRPCChatCLI(t, fake)
	// A page served from a REPL: the REPL's own prompt is not this turn's.
	c.replActive = true

	turn, err := c.RunChatTurnRPC(context.Background(), "gateway", "do the parser tests pass?", nil, RPCChatOpts{})
	require.NoError(t, err)
	assert.Empty(t, turn.HandoffTask)
	assert.Equal(t, "The parser tests need a run.", turn.Reply)
	assert.NotContains(t, systemText(fake), "<coder_handoff>", "nobody can confirm, so the model is not asked")
	assert.True(t, c.coderHandoffActive(), "the REPL's own turns still propose")
}

// The opt-out wins over the surface.
func TestRunChatTurnRPC_HandoffOptOut(t *testing.T) {
	t.Setenv(coderHandoffEnv, "false")
	fake := &rpcChatFakeClient{reply: handoffReply}
	c := newRPCChatCLI(t, fake)

	turn, err := c.RunChatTurnRPC(context.Background(), "web", "do the parser tests pass?", nil, RPCChatOpts{Handoff: true})
	require.NoError(t, err)
	assert.Empty(t, turn.HandoffTask)
	assert.Equal(t, "The parser tests need a run.", turn.Reply)
	assert.NotContains(t, systemText(fake), "<coder_handoff>")
}

// A reply that is only the proposal never becomes an empty assistant turn.
func TestRunChatTurnRPC_TagOnlyReplyKeepsANote(t *testing.T) {
	t.Setenv(coderHandoffEnv, "")
	fake := &rpcChatFakeClient{reply: "<coder_handoff>run the tests</coder_handoff>"}
	c := newRPCChatCLI(t, fake)
	turn, err := c.RunChatTurnRPC(context.Background(), "web", "run the tests", nil, RPCChatOpts{Handoff: true})
	require.NoError(t, err)
	assert.Equal(t, "run the tests", turn.HandoffTask)
	assert.Equal(t, i18n.T("handoff.only_tag"), turn.Reply)
}

// The streamed text ends where the proposal starts, however the provider
// cuts the chunks, and the turn still returns the task.
func TestRunChatTurnRPC_StreamNeverCarriesTheProposal(t *testing.T) {
	t.Setenv(coderHandoffEnv, "")
	fake := &streamingFakeClient{chunks: []string{"Needs a run", ".\n\n<co", "der_hand", "off>run the", " tests</coder_handoff>"}}
	c := newRPCChatCLI(t, &fake.rpcChatFakeClient)
	c.Client = fake
	rec := &chunkRecorder{}
	turn, err := c.RunChatTurnRPC(context.Background(), "web", "run them", nil, RPCChatOpts{Stream: rec, Handoff: true})
	require.NoError(t, err)
	assert.Equal(t, "Needs a run.\n\n", strings.Join(rec.parts, ""))
	assert.Equal(t, "run the tests", turn.HandoffTask)
	assert.Equal(t, "Needs a run.", turn.Reply)
}

// A proposal the reply never closed (output limit, cancel) is dropped whole:
// no markup is stored and half a task is never offered. A mention of the
// tag inside a sentence is ordinary text.
func TestExtractCoderHandoff_UnterminatedProposal(t *testing.T) {
	task, cleaned := extractCoderHandoff("Needs a run.\n\n<coder_handoff>fix the te")
	assert.Empty(t, task)
	assert.Equal(t, "Needs a run.", cleaned)

	task, cleaned = extractCoderHandoff("First.\n<coder_handoff>one</coder_handoff>\n  < Coder_Handoff >tw")
	assert.Equal(t, "one", task)
	assert.Equal(t, "First.", cleaned)

	const prose = "The model ends its reply with a <coder_handoff> line when the request needs the workspace."
	task, cleaned = extractCoderHandoff(prose)
	assert.Empty(t, task)
	assert.Equal(t, prose, cleaned)
}

func TestHandoffStreamFilter(t *testing.T) {
	run := func(chunks ...string) string {
		rec := &chunkRecorder{}
		sink, flush := handoffFilteredSink(rec)
		for _, c := range chunks {
			sink.Chunk(c)
		}
		flush()
		return strings.Join(rec.parts, "")
	}
	assert.Equal(t, "plain text", run("plain ", "text"))
	assert.Equal(t, "a < b and <code>x</code>", run("a <", " b and <co", "de>x</code>"), "a look-alike is released untouched")
	assert.Equal(t, "ends with <", run("ends with ", "<"), "held text is flushed at the end")
	assert.Equal(t, "before ", run("before <coder_handoff>task</coder_handoff> after"), "nothing follows the proposal")
	assert.Equal(t, "x", run("x", "< ", "Coder_Handoff >t</coder_handoff>"), "the parser's tolerance is the filter's")
	assert.Equal(t, "", run("<", "coder_handoff>t</coder_handoff>"))
	assert.Equal(t, "x", run("x<\f\vcoder_handoff>t</coder_handoff>"), "every space the parser skips")

	sink, flush := handoffFilteredSink(nil)
	assert.Nil(t, sink, "no sink: the caller keeps its non-streaming path")
	flush()
}
