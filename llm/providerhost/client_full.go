/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package providerhost

import (
	"context"
	"errors"
	"strings"

	"github.com/diillson/chatcli/llm/catalog"
	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/pkg/providerproto"
)

// ErrNotSupported reports an optional capability the plugin did not declare.
var ErrNotSupported = errors.New("not supported by this provider plugin")

// fullClient adapts a full-mode plugin to chatcli's client interfaces. One
// instance serves one model, like every built-in client.
type fullClient struct {
	plugin *Plugin
	model  string
	usage  client.UsageState
	think  client.ThinkingState
}

var (
	_ client.LLMClient           = (*fullClient)(nil)
	_ client.ToolAwareClient     = (*fullClient)(nil)
	_ client.StreamingClient     = (*fullClient)(nil)
	_ client.ModelLister         = (*fullClient)(nil)
	_ client.TokenCounter        = (*fullClient)(nil)
	_ client.UsageAwareClient    = (*fullClient)(nil)
	_ client.ThinkingAwareClient = (*fullClient)(nil)
)

func (c *fullClient) GetModelName() string {
	return catalog.GetDisplayName(c.plugin.Name(), c.model)
}

// LastUsage implements client.UsageAwareClient.
func (c *fullClient) LastUsage() *models.UsageInfo { return c.usage.LastUsage() }

// LastStopReason implements client.StopReasonAwareClient.
func (c *fullClient) LastStopReason() string { return c.usage.LastStopReason() }

// LastThinking implements client.ThinkingAwareClient.
func (c *fullClient) LastThinking() []models.ThinkingBlock { return c.think.LastThinking() }

// LastThinkingModel implements client.ThinkingAwareClient.
func (c *fullClient) LastThinkingModel() string { return c.think.LastThinkingModel() }

func (c *fullClient) params(prompt string, history []models.Message, tools []models.ToolDefinition, maxTokens int, stream bool) providerproto.ChatParams {
	cred, _ := c.plugin.Credential()
	if maxTokens <= 0 {
		maxTokens = catalog.GetMaxTokens(c.plugin.Name(), c.model, catalog.MaxTokensEnvOverride(c.plugin.Name()))
	}
	return providerproto.ChatParams{
		Credential: cred,
		Model:      c.model,
		Messages:   toProtoMessages(prompt, history),
		Tools:      toProtoTools(tools),
		MaxTokens:  maxTokens,
		Stream:     stream,
	}
}

// SendPrompt implements client.LLMClient.
func (c *fullClient) SendPrompt(ctx context.Context, prompt string, history []models.Message, maxTokens int) (string, error) {
	resp, err := c.send(ctx, c.params(prompt, history, nil, maxTokens, false))
	if err != nil {
		return "", err
	}
	return resp.Content, nil
}

// SupportsNativeTools implements client.ToolAwareClient.
func (c *fullClient) SupportsNativeTools() bool { return c.plugin.info.Capabilities.Chat.Tools }

// SendPromptWithTools implements client.ToolAwareClient.
func (c *fullClient) SendPromptWithTools(ctx context.Context, prompt string, history []models.Message, tools []models.ToolDefinition, maxTokens int) (*models.LLMResponse, error) {
	if !c.SupportsNativeTools() {
		tools = nil
	}
	return c.send(ctx, c.params(prompt, history, tools, maxTokens, false))
}

func (c *fullClient) send(ctx context.Context, p providerproto.ChatParams) (*models.LLMResponse, error) {
	var res providerproto.ChatResult
	if err := c.plugin.host.Call(ctx, providerproto.MethodChatSend, p, &res); err != nil {
		return nil, mapError(c.plugin.Name(), err)
	}
	resp := fromProtoResult(res)
	c.record(resp)
	return resp, nil
}

func (c *fullClient) record(resp *models.LLMResponse) {
	if resp.Usage != nil {
		c.usage.StoreUsage(resp.Usage)
	}
	c.usage.StoreStopReason(resp.StopReason)
	c.think.StoreThinking(c.model, resp.Thinking)
}

// SupportsStreaming implements client.StreamingClient.
func (c *fullClient) SupportsStreaming() bool { return c.plugin.info.Capabilities.Chat.Stream }

// SendPromptStream implements client.StreamingClient. Deltas arrive in
// order; the final chunk carries usage and the stop reason.
func (c *fullClient) SendPromptStream(ctx context.Context, prompt string, history []models.Message, maxTokens int) (<-chan client.StreamChunk, error) {
	if !c.SupportsStreaming() {
		return nil, ErrNotSupported
	}
	out := make(chan client.StreamChunk, 64)
	go func() {
		defer close(out)
		var res providerproto.ChatResult
		err := c.plugin.host.CallStreaming(ctx, providerproto.MethodChatSend, c.params(prompt, history, nil, maxTokens, true), &res,
			func(d providerproto.ChatDelta) {
				if d.Text == "" {
					return
				}
				select {
				case out <- client.StreamChunk{Text: d.Text}:
				case <-ctx.Done():
				}
			})
		if err != nil {
			out <- client.StreamChunk{Done: true, Error: mapError(c.plugin.Name(), err)}
			return
		}
		resp := fromProtoResult(res)
		c.record(resp)
		out <- client.StreamChunk{Done: true, Usage: resp.Usage, StopReason: resp.StopReason}
	}()
	return out, nil
}

// ListModels implements client.ModelLister.
func (c *fullClient) ListModels(ctx context.Context) ([]client.ModelInfo, error) {
	return listModels(ctx, c.plugin)
}

// CountTokens implements client.TokenCounter; without tokens/count the
// caller falls back to its calibrated estimate.
func (c *fullClient) CountTokens(ctx context.Context, prompt string, history []models.Message) (int, error) {
	if !c.plugin.info.Capabilities.CountTokens {
		return 0, ErrNotSupported
	}
	cred, _ := c.plugin.Credential()
	var res providerproto.TokensCountResult
	err := c.plugin.host.Call(ctx, providerproto.MethodTokensCount, providerproto.TokensCountParams{
		Credential: cred, Model: c.model, Messages: toProtoMessages(prompt, history),
	}, &res)
	if err != nil {
		return 0, mapError(c.plugin.Name(), err)
	}
	return res.Tokens, nil
}

// listModels serves ModelLister for both modes: the plugin's live list
// tagged [api], or its cached list tagged [catalog] when the live call
// fails.
func listModels(ctx context.Context, p *Plugin) ([]client.ModelInfo, error) {
	models, err := p.Models(ctx)
	source := client.ModelSourceAPI
	if err != nil {
		if len(models) == 0 {
			return nil, err
		}
		source = client.ModelSourceCatalog
	}
	out := make([]client.ModelInfo, 0, len(models))
	for _, m := range models {
		name := m.DisplayName
		if name == "" {
			name = m.ID
		}
		out = append(out, client.ModelInfo{ID: m.ID, DisplayName: strings.TrimSpace(name), Source: source})
	}
	return out, nil
}
