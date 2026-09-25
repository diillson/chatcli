/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package server

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/diillson/chatcli/llm/catalog"
	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/llm/fallback"
	"github.com/diillson/chatcli/models"
	pb "github.com/diillson/chatcli/proto/chatcli/v1"
	"github.com/diillson/chatcli/utils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// routeClient is a scripted LLM client: replies in order, optional usage,
// optional streaming.
type routeClient struct {
	model   string
	replies []string
	errs    []error
	calls   int32
	usage   *models.UsageInfo
	stop    string
	stream  bool
}

func (c *routeClient) GetModelName() string { return c.model }
func (c *routeClient) next() (string, error) {
	i := int(atomic.AddInt32(&c.calls, 1)) - 1
	var err error
	if i < len(c.errs) {
		err = c.errs[i]
	}
	reply := ""
	if i < len(c.replies) {
		reply = c.replies[i]
	}
	return reply, err
}
func (c *routeClient) SendPrompt(_ context.Context, _ string, _ []models.Message, _ int) (string, error) {
	return c.next()
}
func (c *routeClient) LastUsage() *models.UsageInfo { return c.usage }
func (c *routeClient) LastStopReason() string       { return c.stop }
func (c *routeClient) SupportsStreaming() bool      { return c.stream }
func (c *routeClient) SendPromptStream(_ context.Context, _ string, _ []models.Message, _ int) (<-chan client.StreamChunk, error) {
	reply, err := c.next()
	if err != nil {
		return nil, err
	}
	out := make(chan client.StreamChunk, len(reply)+1)
	for _, r := range reply {
		out <- client.StreamChunk{Text: string(r)}
	}
	out <- client.StreamChunk{Done: true, Usage: c.usage, StopReason: c.stop}
	close(out)
	return out, nil
}

// routeManager hands out scripted clients keyed by "PROVIDER" or
// "PROVIDER:model" (the specific key wins) and counts refreshes.
type routeManager struct {
	mockLLMManager
	byProvider map[string]client.LLMClient
	refreshes  int32
}

func (m *routeManager) GetClient(provider, model string) (client.LLMClient, error) {
	if c, ok := m.byProvider[provider+":"+model]; ok {
		return c, nil
	}
	if c, ok := m.byProvider[provider]; ok {
		return c, nil
	}
	return nil, errors.New("unknown provider " + provider)
}
func (m *routeManager) CreateClientWithKey(provider, model, _ string) (client.LLMClient, error) {
	return m.GetClient(provider, model)
}
func (m *routeManager) RefreshProviders() { atomic.AddInt32(&m.refreshes, 1) }

// chainOf builds a real fallback chain over scripted clients.
func chainOf(entries ...*routeClient) *fallback.Chain {
	fe := make([]fallback.FallbackEntry, 0, len(entries))
	for i, e := range entries {
		fe = append(fe, fallback.FallbackEntry{Provider: "P" + string(rune('A'+i)), Model: e.model, Client: e, Priority: i})
	}
	return fallback.NewChain(zap.NewNop(), fe, fallback.WithMaxRetries(0))
}

func refusalErr() error {
	return &client.EmptyResponseError{Provider: "CLAUDEAI", Model: "claude-fable-5-1", StopReason: client.StopReasonRefusal}
}

func newRouteHandler(mgr *routeManager) *Handler {
	return NewHandler(mgr, nil, zap.NewNop(), "OPENAI", "gpt-6-astra")
}

func TestResolveRoute_Precedence(t *testing.T) {
	def := &routeClient{model: "gpt-6-astra"}
	claude := &routeClient{model: "claude-sonnet-5"}
	mgr := &routeManager{byProvider: map[string]client.LLMClient{"OPENAI": def, "CLAUDEAI": claude}}
	h := newRouteHandler(mgr)

	r, err := h.resolveRoute("", "", "", nil)
	require.NoError(t, err)
	assert.Same(t, def, r.client, "no chain: the server default client")
	assert.Equal(t, "OPENAI", r.provider)
	assert.False(t, r.callerCreds)

	h.SetFallbackChain(chainOf(&routeClient{model: "a"}, &routeClient{model: "b"}))
	r, err = h.resolveRoute("", "", "", nil)
	require.NoError(t, err)
	assert.NotNil(t, r.chain, "nothing named: the fallback chain serves")

	r, err = h.resolveRoute("CLAUDEAI", "", "", nil)
	require.NoError(t, err)
	assert.Nil(t, r.chain, "an explicit provider bypasses the chain")
	assert.Same(t, claude, r.client)
	assert.Equal(t, "CLAUDEAI", r.provider)

	r, err = h.resolveRoute("", "", "sk-caller", nil)
	require.NoError(t, err)
	assert.Nil(t, r.chain, "caller credentials bypass the chain")
	assert.True(t, r.callerCreds)
	assert.Equal(t, "OPENAI", r.provider, "default provider with the caller's key")

	_, err = h.resolveRoute("NOPE", "", "", nil)
	assert.Error(t, err)
}

func TestEffectiveMaxTokens_RequestThenEnvThenCatalog(t *testing.T) {
	t.Setenv("ANTHROPIC_MAX_TOKENS", "")
	h := newRouteHandler(&routeManager{byProvider: map[string]client.LLMClient{}})
	r := llmRoute{provider: catalog.ProviderClaudeAI, model: "claude-sonnet-4-6"}
	assert.Equal(t, 321, h.effectiveMaxTokens(321, r))
	assert.Equal(t, catalog.GetMaxTokens(catalog.ProviderClaudeAI, "claude-sonnet-4-6", 0), h.effectiveMaxTokens(0, r), "the catalog ceiling replaces the old implicit 0")
	t.Setenv("ANTHROPIC_MAX_TOKENS", "1234")
	assert.Equal(t, 1234, h.effectiveMaxTokens(0, r))
}

func TestComplete_AttributesUsageAndStopReason(t *testing.T) {
	def := &routeClient{model: "gpt-6-astra", replies: []string{"hello"}, usage: &models.UsageInfo{PromptTokens: 7, CompletionTokens: 2, IsReal: true}, stop: "end_turn"}
	h := newRouteHandler(&routeManager{byProvider: map[string]client.LLMClient{"OPENAI": def}})
	r, err := h.resolveRoute("", "", "", nil)
	require.NoError(t, err)
	res, err := h.complete(context.Background(), r, "hi", nil, 100)
	require.NoError(t, err)
	assert.Equal(t, "hello", res.text)
	assert.Equal(t, "OPENAI", res.provider)
	assert.Equal(t, "gpt-6-astra", res.model)
	assert.Equal(t, 7, res.usage.PromptTokens)
	assert.True(t, res.usage.IsReal)
	assert.Equal(t, "end_turn", res.stopReason)

	pu := usageToProto(res.provider, res.model, res.usage)
	assert.Equal(t, int32(7), pu.PromptTokens)
	assert.False(t, pu.Estimated)
	back := protoToUsage(pu)
	assert.Equal(t, 9, back.TotalTokens)
	assert.True(t, back.IsReal)
	assert.Nil(t, usageToProto("OPENAI", "gpt-6-astra", nil))
	assert.Nil(t, protoToUsage(nil))
}

func TestComplete_EstimatesUsageWhenProviderReportsNone(t *testing.T) {
	plain := &mockLLMClient{}
	plain.On("GetModelName").Return("m")
	plain.On("SendPrompt", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return("four", nil)
	mgr := &mockLLMManager{}
	mgr.On("GetClient", "OPENAI", "gpt-6-astra").Return(plain, nil)
	h := NewHandler(mgr, nil, zap.NewNop(), "OPENAI", "gpt-6-astra")
	r, err := h.resolveRoute("", "", "", nil)
	require.NoError(t, err)
	res, err := h.complete(context.Background(), r, "12345678", []models.Message{{Role: "user", Content: "abcd"}}, 10)
	require.NoError(t, err)
	require.NotNil(t, res.usage)
	assert.False(t, res.usage.IsReal)
	assert.Equal(t, 3, res.usage.PromptTokens, "prompt plus history characters over four")
	assert.Equal(t, 1, res.usage.CompletionTokens)
}

func TestComplete_RefreshesServerCredentialsOnceAndRetries(t *testing.T) {
	expired := &routeClient{model: "gpt-6-astra", errs: []error{&utils.APIError{StatusCode: 401, Message: "expired"}}, replies: []string{"", "after refresh"}}
	mgr := &routeManager{byProvider: map[string]client.LLMClient{"OPENAI": expired}}
	h := newRouteHandler(mgr)
	r, err := h.resolveRoute("", "", "", nil)
	require.NoError(t, err)

	res, err := h.complete(context.Background(), r, "hi", nil, 10)
	require.NoError(t, err)
	assert.Equal(t, "after refresh", res.text)
	assert.Equal(t, int32(1), atomic.LoadInt32(&mgr.refreshes))

	// A second 401 inside the throttle window must not refresh again.
	expired.calls, expired.errs, expired.replies = 0, []error{&utils.APIError{StatusCode: 403}}, []string{"", "ok"}
	_, err = h.complete(context.Background(), r, "hi", nil, 10)
	require.NoError(t, err)
	assert.Equal(t, int32(1), atomic.LoadInt32(&mgr.refreshes), "throttled")

	h.authRefreshAt = time.Now().Add(-2 * authRefreshMinInterval)
	expired.calls, expired.errs, expired.replies = 0, []error{&utils.APIError{StatusCode: 401}}, []string{"", "ok"}
	_, err = h.complete(context.Background(), r, "hi", nil, 10)
	require.NoError(t, err)
	assert.Equal(t, int32(2), atomic.LoadInt32(&mgr.refreshes), "refreshes again once the window passed")
}

func TestComplete_NeverRefreshesCallerCredentials(t *testing.T) {
	expired := &routeClient{model: "gpt-6-astra", errs: []error{&utils.APIError{StatusCode: 401, Message: "bad key"}}}
	mgr := &routeManager{byProvider: map[string]client.LLMClient{"OPENAI": expired}}
	h := newRouteHandler(mgr)
	r, err := h.resolveRoute("", "", "sk-caller", nil)
	require.NoError(t, err)
	_, err = h.complete(context.Background(), r, "hi", nil, 10)
	require.Error(t, err)
	assert.Equal(t, int32(0), atomic.LoadInt32(&mgr.refreshes), "the caller owns its credential")
	assert.Equal(t, int32(1), atomic.LoadInt32(&expired.calls))
}

func TestComplete_RetriesRefusalOnSiblingWithSameCredentials(t *testing.T) {
	fable := &routeClient{model: "claude-fable-5-1", errs: []error{refusalErr()}}
	opus := &routeClient{model: "claude-opus-5", replies: []string{"sibling answered"}}
	mgr := &routeManager{byProvider: map[string]client.LLMClient{
		"CLAUDEAI":                  fable,
		"CLAUDEAI:claude-opus-5":    opus,
		"CLAUDEAI:claude-fable-5-1": fable,
	}}
	h := NewHandler(mgr, nil, zap.NewNop(), "CLAUDEAI", "claude-fable-5-1")

	r, err := h.resolveRoute("", "", "sk-caller", nil)
	require.NoError(t, err)
	res, err := h.complete(context.Background(), r, "hi", nil, 10)
	require.NoError(t, err)
	assert.Equal(t, "sibling answered", res.text)
	assert.Equal(t, "claude-opus-5", res.model)
	assert.Equal(t, "CLAUDEAI", res.provider)
	assert.Equal(t, int32(1), atomic.LoadInt32(&fable.calls), "resent once, on the sibling, never on the model that refused")
	assert.Equal(t, int32(1), atomic.LoadInt32(&opus.calls))
	assert.Equal(t, int32(0), atomic.LoadInt32(&mgr.refreshes), "a refusal is not an auth error")
}

func TestComplete_RefusalWithoutSiblingSurfacesTheError(t *testing.T) {
	gpt := &routeClient{model: "gpt-6-astra", errs: []error{refusalErr()}}
	mgr := &routeManager{byProvider: map[string]client.LLMClient{"OPENAI": gpt}}
	h := newRouteHandler(mgr)
	r, err := h.resolveRoute("", "", "", nil)
	require.NoError(t, err)
	_, err = h.complete(context.Background(), r, "hi", nil, 10)
	require.Error(t, err)
	assert.True(t, client.IsRefusal(err))
	assert.Equal(t, int32(1), atomic.LoadInt32(&gpt.calls))
}

func TestStream_ForwardsChunksAndFinalUsage(t *testing.T) {
	sc := &routeClient{model: "gpt-6-astra", replies: []string{"abc"}, stream: true, usage: &models.UsageInfo{PromptTokens: 1, CompletionTokens: 3, IsReal: true}, stop: "end_turn"}
	h := newRouteHandler(&routeManager{byProvider: map[string]client.LLMClient{"OPENAI": sc}})
	r, err := h.resolveRoute("", "", "", nil)
	require.NoError(t, err)
	var got []string
	res, err := h.stream(context.Background(), r, "hi", nil, 10, func(c string) error { got = append(got, c); return nil })
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "b", "c"}, got, "real chunks, not a post-hoc split")
	assert.Equal(t, "abc", res.text)
	assert.Equal(t, 3, res.usage.CompletionTokens)
	assert.Equal(t, "end_turn", res.stopReason)
}

func TestStream_NonStreamingRouteDeliversOneChunk(t *testing.T) {
	plain := &routeClient{model: "gpt-6-astra", replies: []string{"whole"}}
	h := newRouteHandler(&routeManager{byProvider: map[string]client.LLMClient{"OPENAI": plain}})
	r, err := h.resolveRoute("", "", "", nil)
	require.NoError(t, err)
	var got []string
	res, err := h.stream(context.Background(), r, "hi", nil, 10, func(c string) error { got = append(got, c); return nil })
	require.NoError(t, err)
	assert.Equal(t, []string{"whole"}, got)
	assert.Equal(t, "whole", res.text)
}

func TestStream_ChainAttributionComesFromTheEntryThatAnswered(t *testing.T) {
	down := &routeClient{model: "model-a", stream: true, errs: []error{errors.New("503 overloaded")}}
	up := &routeClient{model: "model-b", stream: true, replies: []string{"via b"}}
	h := newRouteHandler(&routeManager{byProvider: map[string]client.LLMClient{}})
	h.SetFallbackChain(chainOf(down, up))
	r, err := h.resolveRoute("", "", "", nil)
	require.NoError(t, err)
	var got string
	res, err := h.stream(context.Background(), r, "hi", nil, 10, func(c string) error { got += c; return nil })
	require.NoError(t, err)
	assert.Equal(t, "via b", got)
	assert.Equal(t, "PB", res.provider, "the entry that answered, not the chain's first entry")
	assert.Equal(t, "model-b", res.model)

	// Unary through the chain attributes the same way.
	down.calls, up.calls = 0, 0
	res, err = h.complete(context.Background(), r, "hi", nil, 10)
	require.NoError(t, err)
	assert.Equal(t, "PB", res.provider)
}

// fakeStreamServer captures StreamPrompt messages.
type fakeStreamServer struct {
	grpc.ServerStream
	ctx  context.Context
	sent []*pb.StreamPromptResponse
}

func (f *fakeStreamServer) Context() context.Context { return f.ctx }
func (f *fakeStreamServer) Send(m *pb.StreamPromptResponse) error {
	f.sent = append(f.sent, m)
	return nil
}
func (f *fakeStreamServer) SetHeader(metadata.MD) error  { return nil }
func (f *fakeStreamServer) SendHeader(metadata.MD) error { return nil }
func (f *fakeStreamServer) SetTrailer(metadata.MD)       {}

func TestStreamPrompt_EndToEndMessages(t *testing.T) {
	sc := &routeClient{model: "gpt-6-astra", replies: []string{"hi!"}, stream: true, usage: &models.UsageInfo{PromptTokens: 2, CompletionTokens: 1, IsReal: true}, stop: "end_turn"}
	h := newRouteHandler(&routeManager{byProvider: map[string]client.LLMClient{"OPENAI": sc}})
	srv := &fakeStreamServer{ctx: context.Background()}
	require.NoError(t, h.StreamPrompt(&pb.StreamPromptRequest{Prompt: "hello"}, srv))
	require.Len(t, srv.sent, 4, "three chunks plus the done message")
	assert.Equal(t, "h", srv.sent[0].Chunk)
	assert.False(t, srv.sent[0].Done)
	last := srv.sent[3]
	assert.True(t, last.Done)
	assert.Empty(t, last.Chunk)
	assert.Equal(t, "OPENAI", last.Provider)
	assert.Equal(t, "gpt-6-astra", last.Model)
	assert.Equal(t, int32(2), last.Usage.PromptTokens)
	assert.Equal(t, "end_turn", last.StopReason)

	_, err := h.SendPrompt(context.Background(), &pb.SendPromptRequest{})
	assert.Error(t, err, "empty prompt is rejected")
	err = h.StreamPrompt(&pb.StreamPromptRequest{}, srv)
	assert.Error(t, err)
}

func TestSendPrompt_ResponseCarriesUsageAndAttribution(t *testing.T) {
	def := &routeClient{model: "gpt-6-astra", replies: []string{"yo"}, usage: &models.UsageInfo{PromptTokens: 5, CompletionTokens: 1, CacheReadInputTokens: 4, IsReal: true}, stop: "end_turn"}
	h := newRouteHandler(&routeManager{byProvider: map[string]client.LLMClient{"OPENAI": def}})
	resp, err := h.SendPrompt(context.Background(), &pb.SendPromptRequest{Prompt: "hi", MaxTokens: 0})
	require.NoError(t, err)
	assert.Equal(t, "yo", resp.Response)
	assert.Equal(t, "OPENAI", resp.Provider)
	assert.Equal(t, "gpt-6-astra", resp.Model)
	assert.Equal(t, int32(4), resp.Usage.CacheReadTokens)
	assert.Equal(t, "end_turn", resp.StopReason)
}

func TestSessionReplyMetadata(t *testing.T) {
	md := sessionReplyMetadata(turnResult{provider: "CLAUDEAI", model: "claude-sonnet-5", stopReason: "end_turn",
		usage: &models.UsageInfo{PromptTokens: 3, CompletionTokens: 2, CacheReadInputTokens: 1, CacheCreationInputTokens: 1, IsReal: true}})
	assert.Equal(t, "CLAUDEAI", md["provider"])
	assert.Equal(t, "claude-sonnet-5", md["model"])
	assert.Equal(t, "3", md["prompt_tokens"])
	assert.Equal(t, "1", md["cache_read_tokens"])
	assert.Equal(t, "1", md["cache_write_tokens"])
	assert.Equal(t, "false", md["usage_estimated"])
	assert.Equal(t, "end_turn", md["stop_reason"])
	md = sessionReplyMetadata(turnResult{provider: "P", model: "m"})
	_, ok := md["prompt_tokens"]
	assert.False(t, ok)
}

func TestIsAuthError(t *testing.T) {
	assert.True(t, isAuthError(&utils.APIError{StatusCode: 401}))
	assert.True(t, isAuthError(&utils.APIError{StatusCode: 403}))
	assert.False(t, isAuthError(&utils.APIError{StatusCode: 500}))
	assert.False(t, isAuthError(errors.New("nope")))
	assert.False(t, isAuthError(nil))
}

func TestUsageToProto_PricesWithTheSharedEngine(t *testing.T) {
	u := &models.UsageInfo{PromptTokens: 1_000_000, CompletionTokens: 1_000_000, IsReal: true}
	priced := usageToProto("OPENAI", "gpt-6-astra", u)
	require.NotNil(t, priced)
	assert.True(t, priced.CostKnown, "a catalog model has a price")
	assert.Greater(t, priced.CostUsd, 0.0)
	unknown := usageToProto("OPENAI", "no-such-model-ever", u)
	assert.False(t, unknown.CostKnown, "an unpriced model reports no cost rather than a guess")
	assert.Equal(t, 0.0, unknown.CostUsd)
	assert.Equal(t, int32(1_000_000), unknown.PromptTokens, "tokens are reported either way")
}
