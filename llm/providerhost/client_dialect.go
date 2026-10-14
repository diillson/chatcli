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
	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/llm/openai"
	"github.com/diillson/chatcli/pkg/providerproto"
	"go.uber.org/zap"
)

// dialectRetries mirror the built-in providers' defaults.
const (
	dialectMaxAttempts = 3
	dialectBackoff     = 2 * time.Second
)

// dialectClient is chatcli's built-in OpenAI chat-completions client aimed
// at the plugin's backend. Native tools, streaming, prompt-cache usage,
// token counting and keep-alive all come from the embedded client; only the
// model list is served by the plugin.
type dialectClient struct {
	*openai.OpenAIClient
	plugin *Plugin
}

// ListModels serves the plugin's list instead of the backend's /models.
func (c *dialectClient) ListModels(ctx context.Context) ([]client.ModelInfo, error) {
	return listModels(ctx, c.plugin)
}

// placeholderToken satisfies the embedded client's Authorization header;
// headerTransport replaces or removes it before the request leaves.
const placeholderToken = "provider-plugin"

func newDialectClient(p *Plugin, model string, logger *zap.Logger) (client.LLMClient, error) {
	api := p.info.Dialect.API
	if m, ok := p.Model(model); ok && m.Dialect != "" {
		api = m.Dialect
	}
	if !supportedDialect(api) {
		return nil, fmt.Errorf("%s: dialect %q is not supported by this chatcli", p.Name(), api)
	}
	chatURL := strings.TrimRight(p.info.Dialect.BaseURL, "/") + "/chat/completions"
	tokens := auth.NewStaticTokenProvider(placeholderToken, auth.AuthModeAPIKey, auth.ProviderPlugin)
	inner := openai.NewOpenAIClientWithOptions(tokens, model, logger, dialectMaxAttempts, dialectBackoff, openai.ClientOptions{
		ChatURL:         chatURL,
		CatalogProvider: p.Name(),
		WrapTransport: func(next http.RoundTripper) http.RoundTripper {
			return &headerTransport{plugin: p, model: model, next: next}
		},
	})
	return &dialectClient{OpenAIClient: inner, plugin: p}, nil
}

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
	providesAuth := false
	for k, v := range headers {
		if strings.EqualFold(k, "Authorization") {
			providesAuth = true
		}
		r.Header.Set(k, v)
	}
	if m, ok := t.plugin.Model(t.model); ok {
		for k, v := range m.Headers {
			if strings.EqualFold(k, "Authorization") {
				providesAuth = true
			}
			r.Header.Set(k, v)
		}
	}
	if !providesAuth {
		// The plugin authenticates with its own headers; never send the
		// embedded client's placeholder bearer.
		r.Header.Del("Authorization")
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
