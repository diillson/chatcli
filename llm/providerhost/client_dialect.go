/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package providerhost

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/diillson/chatcli/auth"
	"github.com/diillson/chatcli/llm/claudeai"
	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/llm/googleai"
	"github.com/diillson/chatcli/llm/openai"
	"github.com/diillson/chatcli/llm/openairesponses"
	"github.com/diillson/chatcli/pkg/providerproto"
	"go.uber.org/zap"
)

// dialectRetries mirror the built-in providers' defaults.
const (
	dialectMaxAttempts = 3
	dialectBackoff     = 2 * time.Second
)

// Dialect clients are chatcli's built-in clients aimed at the plugin's
// backend. Native tools, streaming, prompt-cache usage, token counting and
// keep-alive all come from the embedded client; only the model list is
// served by the plugin. One wrapper per client type, so each keeps the
// optional interfaces its embedded client implements.
type dialectOpenAIChat struct {
	*openai.OpenAIClient
	plugin *Plugin
}

func (c *dialectOpenAIChat) ListModels(ctx context.Context) ([]client.ModelInfo, error) {
	return listModels(ctx, c.plugin)
}

type dialectOpenAIResponses struct {
	*openairesponses.OpenAIResponsesClient
	plugin *Plugin
}

func (c *dialectOpenAIResponses) ListModels(ctx context.Context) ([]client.ModelInfo, error) {
	return listModels(ctx, c.plugin)
}

type dialectAnthropic struct {
	*claudeai.ClaudeClient
	plugin *Plugin
}

func (c *dialectAnthropic) ListModels(ctx context.Context) ([]client.ModelInfo, error) {
	return listModels(ctx, c.plugin)
}

type dialectGemini struct {
	*googleai.GeminiClient
	plugin *Plugin
}

func (c *dialectGemini) ListModels(ctx context.Context) ([]client.ModelInfo, error) {
	return listModels(ctx, c.plugin)
}

// placeholderToken satisfies the embedded client's own auth header;
// headerTransport replaces or removes it before the request leaves.
const placeholderToken = "provider-plugin"

func newDialectClient(p *Plugin, model string, logger *zap.Logger) (client.LLMClient, error) {
	api := p.info.Dialect.API
	entry, listed := p.Model(model)
	if listed && entry.Dialect != "" {
		api = entry.Dialect
	}
	if !supportedDialect(api) {
		return nil, fmt.Errorf("%s: dialect %q is not supported by this chatcli", p.Name(), api)
	}
	base := strings.TrimRight(p.info.Dialect.BaseURL, "/")
	tokens := auth.NewStaticTokenProvider(placeholderToken, auth.AuthModeAPIKey, auth.ProviderPlugin)
	wrap := func(next http.RoundTripper) http.RoundTripper {
		return &headerTransport{plugin: p, model: model, next: next}
	}
	switch api {
	case providerproto.DialectOpenAIResponses:
		inner := openairesponses.NewOpenAIResponsesClientWithOptions(tokens, model, logger, dialectMaxAttempts, dialectBackoff,
			openairesponses.ClientOptions{ResponsesURL: base + "/responses", WrapTransport: wrap, CatalogProvider: p.Name()})
		return &dialectOpenAIResponses{OpenAIResponsesClient: inner, plugin: p}, nil
	case providerproto.DialectAnthropicMessages:
		upstream := ""
		if listed && entry.Upstream != nil {
			upstream = entry.Upstream.Model
		}
		inner := claudeai.NewClaudeClientWithOptions(tokens, model, logger, dialectMaxAttempts, dialectBackoff,
			claudeai.ClientOptions{MessagesURL: base + "/messages", WrapTransport: wrap, CatalogProvider: p.Name(), UpstreamModel: upstream})
		return &dialectAnthropic{ClaudeClient: inner, plugin: p}, nil
	case providerproto.DialectGemini:
		inner := googleai.NewGeminiClientWithOptions(tokens, model, logger, dialectMaxAttempts, dialectBackoff,
			googleai.ClientOptions{BaseURL: base, WrapTransport: wrap, CatalogProvider: p.Name()})
		return &dialectGemini{GeminiClient: inner, plugin: p}, nil
	default:
		inner := openai.NewOpenAIClientWithOptions(tokens, model, logger, dialectMaxAttempts, dialectBackoff,
			openai.ClientOptions{ChatURL: base + "/chat/completions", WrapTransport: wrap, CatalogProvider: p.Name()})
		return &dialectOpenAIChat{OpenAIClient: inner, plugin: p}, nil
	}
}

// builtinAuthHeaders are the credential headers the embedded clients set.
var builtinAuthHeaders = []string{"Authorization", "X-Api-Key", "X-Goog-Api-Key", "Api-Key"}

// headerTransport attaches the plugin's auth/token headers (and the
// model's own headers) to every request. It sits below the request logger,
// so those headers never reach the HTTP log. On 401/403 it refetches the
// token once and replays the request.
type headerTransport struct {
	plugin *Plugin
	model  string
	next   http.RoundTripper
}

func (t *headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.send(req, false)
	if err != nil || (resp.StatusCode != http.StatusUnauthorized && resp.StatusCode != http.StatusForbidden) {
		return resp, err
	}
	if !t.plugin.info.Capabilities.Auth.Token || (req.Body != nil && req.GetBody == nil) {
		return resp, nil
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	_ = resp.Body.Close()
	return t.send(req, true)
}

func (t *headerTransport) send(req *http.Request, force bool) (*http.Response, error) {
	headers, err := t.plugin.headersFor(req.Context(), force)
	if err != nil {
		return nil, err
	}
	r := req.Clone(req.Context())
	if req.GetBody != nil {
		body, err := req.GetBody()
		if err != nil {
			return nil, err
		}
		r.Body = body
	}
	// The embedded clients authenticate with one of these headers and the
	// placeholder token; any of them the plugin does not set itself is
	// removed, so the placeholder never leaves the process.
	for _, h := range builtinAuthHeaders {
		r.Header.Del(h)
	}
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	if m, ok := t.plugin.Model(t.model); ok {
		for k, v := range m.Headers {
			r.Header.Set(k, v)
		}
	}
	return t.next.RoundTrip(r)
}

// NewClient returns a client for model, the factory the manager registers.
func (p *Plugin) NewClient(model string) (client.LLMClient, error) {
	switch p.info.Mode {
	case providerproto.ModeDialect:
		return newDialectClient(p, model, p.logger)
	default:
		return &fullClient{plugin: p, model: model}, nil
	}
}
