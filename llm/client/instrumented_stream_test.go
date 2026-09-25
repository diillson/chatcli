/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package client

import (
	"context"
	"errors"
	"testing"

	"github.com/diillson/chatcli/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// streamingMock is a mockLLMClient that also streams and reports usage.
type streamingMock struct {
	mockLLMClient
	chunks   []string
	openErr  error
	chunkErr error
	usage    *models.UsageInfo
	stop     string
}

func (m *streamingMock) SupportsStreaming() bool      { return true }
func (m *streamingMock) LastUsage() *models.UsageInfo { return m.usage }
func (m *streamingMock) LastStopReason() string       { return m.stop }
func (m *streamingMock) SendPromptStream(_ context.Context, _ string, _ []models.Message, _ int) (<-chan StreamChunk, error) {
	if m.openErr != nil {
		return nil, m.openErr
	}
	out := make(chan StreamChunk, len(m.chunks)+1)
	for _, c := range m.chunks {
		out <- StreamChunk{Text: c}
	}
	if m.chunkErr != nil {
		out <- StreamChunk{Error: m.chunkErr}
	} else {
		out <- StreamChunk{Done: true, Usage: m.usage, StopReason: m.stop}
	}
	close(out)
	return out, nil
}

type tokenRecorder struct {
	mockRecorder
	tokens map[string]int64
}

func (r *tokenRecorder) RecordTokens(_, _, kind string, n int64) {
	if r.tokens == nil {
		r.tokens = map[string]int64{}
	}
	r.tokens[kind] += n
}

func TestInstrumentedClient_StreamsAndRecordsOnDone(t *testing.T) {
	inner := &streamingMock{mockLLMClient: mockLLMClient{model: "m"}, chunks: []string{"a", "b"},
		usage: &models.UsageInfo{PromptTokens: 10, CompletionTokens: 4, CacheReadInputTokens: 6}, stop: "end_turn"}
	rec := &tokenRecorder{}
	ic := NewInstrumentedClient(inner, rec, "P")

	assert.True(t, ic.SupportsStreaming())
	sc, ok := AsStreamingClient(ic)
	require.True(t, ok, "the wrapper must not hide the streaming capability")
	ch, err := sc.SendPromptStream(context.Background(), "p", nil, 0)
	require.NoError(t, err)
	text, usage, stop, err := DrainStream(ch)
	require.NoError(t, err)
	assert.Equal(t, "ab", text)
	assert.Equal(t, 10, usage.PromptTokens)
	assert.Equal(t, "end_turn", stop)
	require.Len(t, rec.requests, 1)
	assert.Equal(t, "success", rec.requests[0].status)
	assert.Equal(t, int64(10), rec.tokens["input"])
	assert.Equal(t, int64(6), rec.tokens["cache_read"])

	assert.Equal(t, 10, ic.LastUsage().PromptTokens, "usage passes through the wrapper")
	assert.Equal(t, "end_turn", ic.LastStopReason())
	_, ok = AsUsageAware(ic)
	assert.True(t, ok)
	assert.Same(t, inner, ic.Unwrap())
}

func TestInstrumentedClient_StreamErrorsAreRecordedOnce(t *testing.T) {
	inner := &streamingMock{mockLLMClient: mockLLMClient{model: "m"}, chunks: []string{"x"}, chunkErr: errors.New("rate limit exceeded")}
	rec := &mockRecorder{}
	ic := NewInstrumentedClient(inner, rec, "P")
	ch, err := ic.SendPromptStream(context.Background(), "p", nil, 0)
	require.NoError(t, err)
	_, _, _, err = DrainStream(ch)
	require.Error(t, err)
	require.Len(t, rec.requests, 1)
	assert.Equal(t, "error", rec.requests[0].status)
	require.Len(t, rec.errors, 1)
	assert.Equal(t, "rate_limit", rec.errors[0].errorType)
}

func TestInstrumentedClient_StreamOpenFailureIsRecorded(t *testing.T) {
	inner := &streamingMock{mockLLMClient: mockLLMClient{model: "m"}, openErr: errors.New("401 unauthorized")}
	rec := &mockRecorder{}
	ic := NewInstrumentedClient(inner, rec, "P")
	_, err := ic.SendPromptStream(context.Background(), "p", nil, 0)
	require.Error(t, err)
	require.Len(t, rec.errors, 1)
}

func TestInstrumentedClient_NonStreamingInnerStaysNonStreaming(t *testing.T) {
	ic := NewInstrumentedClient(&mockLLMClient{model: "m"}, &mockRecorder{}, "P")
	assert.False(t, ic.SupportsStreaming())
	_, ok := AsStreamingClient(ic)
	assert.False(t, ok)
	_, err := ic.SendPromptStream(context.Background(), "p", nil, 0)
	assert.Error(t, err)
	assert.Nil(t, ic.LastUsage())
	assert.Empty(t, ic.LastStopReason())
}
