/*
 * ChatCLI - Anthropic prompt-cache keep-alive
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package claudeai

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"

	"github.com/diillson/chatcli/auth"
	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/utils"
	"go.uber.org/zap"
)

// rememberRequest keeps the body of the last request that reached the
// API on the key/token paths, so a keep-alive can re-send the same
// prefix. The OAuth path streams and never carries cache markers, so
// there is nothing there to keep warm.
func (c *ClaudeClient) rememberRequest(body []byte) {
	if c == nil || c.provider.Mode() == auth.AuthModeOAuth {
		return
	}
	c.lastRequest.Remember(body)
}

// KeepPromptCacheWarm re-sends the last request with max_tokens: 0. The
// API runs the prefill, refreshes the cache entry at the breakpoints the
// request carries, and returns no content — only usage, which is what
// comes back so the caller can book the cache read. The body is derived
// by client.KeepAliveRequestBody: thinking and effort kept, task budget
// and streaming removed.
func (c *ClaudeClient) KeepPromptCacheWarm(ctx context.Context) (*models.UsageInfo, error) {
	if c == nil || c.provider.Mode() == auth.AuthModeOAuth {
		return nil, client.ErrPromptCacheKeepAliveUnsupported
	}
	last, ok := c.lastRequest.Take()
	if !ok {
		return nil, client.ErrPromptCacheKeepAliveUnsupported
	}
	body, err := client.KeepAliveRequestBody(last, 0)
	if err != nil {
		return nil, err
	}

	resp, err := auth.DoWithRefresh(ctx, c.provider, func(token string) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiURL, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Add("Content-Type", oauthContentType)
		c.applyAuthHeaders(req, token)
		return c.client.Do(req)
	})
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &utils.APIError{StatusCode: resp.StatusCode, Message: utils.SanitizeSensitiveText(string(raw))}
	}
	// The usage state is per instance and the turn that produced the
	// remembered request has already been booked, so parsing into it is
	// safe; the caller reads the result back from the return value.
	c.resetUsage()
	c.recordUsageFromBody(raw)
	usage := c.LastUsage()
	if usage == nil {
		return nil, fmt.Errorf("claudeai: keep-alive response carried no usage")
	}
	c.logger.Debug("claudeai: prompt cache kept warm",
		zap.Int("cache_read", usage.CacheReadInputTokens),
		zap.Int("cache_write", usage.CacheCreationInputTokens),
		zap.String("model", c.model))
	return usage, nil
}
