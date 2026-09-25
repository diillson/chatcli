/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package fallback

import (
	"context"
	"errors"
	"testing"

	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// mockStreamClient streams a scripted sequence of chunks; openErr fails the
// call before any chunk, firstErr fails on the first chunk, lateErr fails
// after text was delivered.
type mockStreamClient struct {
	mockClient
	chunks   []string
	openErr  error
	firstErr error
	lateErr  error
	usage    *models.UsageInfo
}

func (m *mockStreamClient) SupportsStreaming() bool { return true }
func (m *mockStreamClient) LastUsage() *models.UsageInfo {
	return m.usage
}
func (m *mockStreamClient) SendPromptStream(_ context.Context, _ string, _ []models.Message, _ int) (<-chan client.StreamChunk, error) {
	m.calls++
	if m.openErr != nil {
		return nil, m.openErr
	}
	out := make(chan client.StreamChunk, len(m.chunks)+2)
	go func() {
		defer close(out)
		if m.firstErr != nil {
			out <- client.StreamChunk{Error: m.firstErr}
			return
		}
		for i, c := range m.chunks {
			out <- client.StreamChunk{Text: c}
			if m.lateErr != nil && i == 0 {
				out <- client.StreamChunk{Error: m.lateErr}
				return
			}
		}
		out <- client.StreamChunk{Done: true, Usage: m.usage, StopReason: "end_turn"}
	}()
	return out, nil
}

func drain(t *testing.T, ch <-chan client.StreamChunk) (string, *models.UsageInfo, error) {
	t.Helper()
	text, usage, _, err := client.DrainStream(ch)
	return text, usage, err
}

func TestChainStream_FailsOverBeforeFirstChunk(t *testing.T) {
	dead := &mockStreamClient{mockClient: mockClient{model: "a"}, openErr: errors.New("503 overloaded")}
	rejects := &mockStreamClient{mockClient: mockClient{model: "b"}, firstErr: errors.New("429 rate limit")}
	good := &mockStreamClient{mockClient: mockClient{model: "c"}, chunks: []string{"hel", "lo"}, usage: &models.UsageInfo{PromptTokens: 3, CompletionTokens: 2, IsReal: true}}
	chain := NewChain(zap.NewNop(), []FallbackEntry{
		{Provider: "A", Model: "a", Client: dead},
		{Provider: "B", Model: "b", Client: rejects},
		{Provider: "C", Model: "c", Client: good},
	})
	assert.True(t, chain.SupportsStreaming())

	ch, err := chain.SendPromptStream(context.Background(), "hi", nil, 0)
	require.NoError(t, err)
	text, usage, err := drain(t, ch)
	require.NoError(t, err)
	assert.Equal(t, "hello", text)
	require.NotNil(t, usage)
	assert.Equal(t, 3, usage.PromptTokens)
	p, m, ok := chain.LastServedEntry()
	assert.True(t, ok)
	assert.Equal(t, "C", p)
	assert.Equal(t, "c", m)
	assert.Equal(t, 1, dead.calls)
	assert.Equal(t, 1, rejects.calls)
	assert.False(t, chain.isAvailable("A"), "the entry that failed is cooled down")
}

func TestChainStream_NoFailoverOnceTextFlowed(t *testing.T) {
	partial := &mockStreamClient{mockClient: mockClient{model: "a"}, chunks: []string{"par", "tial"}, lateErr: errors.New("connection reset")}
	spare := &mockStreamClient{mockClient: mockClient{model: "b"}, chunks: []string{"never"}}
	chain := NewChain(zap.NewNop(), []FallbackEntry{
		{Provider: "A", Model: "a", Client: partial},
		{Provider: "B", Model: "b", Client: spare},
	})
	ch, err := chain.SendPromptStream(context.Background(), "hi", nil, 0)
	require.NoError(t, err)
	text, _, err := drain(t, ch)
	assert.Equal(t, "par", text)
	assert.EqualError(t, err, "connection reset")
	assert.Equal(t, 0, spare.calls, "a second provider must not restart a reply the caller already saw")
}

func TestChainStream_NonStreamingEntryAnswersAsOneChunk(t *testing.T) {
	plain := &mockClient{model: "p", response: "whole reply"}
	chain := NewChain(zap.NewNop(), []FallbackEntry{{Provider: "P", Model: "p", Client: plain}})
	assert.False(t, chain.SupportsStreaming())
	ch, err := chain.SendPromptStream(context.Background(), "hi", nil, 0)
	require.NoError(t, err)
	text, _, err := drain(t, ch)
	require.NoError(t, err)
	assert.Equal(t, "whole reply", text)
	assert.Equal(t, 1, plain.calls)
}

func TestChainStream_AllEntriesDown(t *testing.T) {
	chain := NewChain(zap.NewNop(), []FallbackEntry{
		{Provider: "A", Model: "a", Client: &mockStreamClient{mockClient: mockClient{model: "a"}, openErr: errors.New("boom")}},
	})
	_, err := chain.SendPromptStream(context.Background(), "hi", nil, 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "boom")
}

func TestChainStream_HonoursCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	chain := NewChain(zap.NewNop(), []FallbackEntry{{Provider: "A", Model: "a", Client: &mockClient{model: "a", response: "x"}}})
	_, err := chain.SendPromptStream(ctx, "hi", nil, 0)
	assert.ErrorIs(t, err, context.Canceled)
}
