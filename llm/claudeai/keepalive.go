/*
 * ChatCLI - Anthropic prompt-cache keep-alive
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package claudeai

import (
	"bytes"
	"context"
	"encoding/json"
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
	c.lastRequestMu.Lock()
	c.lastRequest = string(body)
	c.lastRequestMu.Unlock()
}

// KeepPromptCacheWarm re-sends the last request with max_tokens: 0. The
// API runs the prefill, refreshes the cache entry at the breakpoints the
// request carries, and returns no content — only usage, which is what
// comes back so the caller can book the cache read.
//
// The body travels as it was, thinking and effort included: those are
// part of the cache key on every current model, so a refresh that
// dropped them would write a new entry instead of refreshing this one.
// Only the task budget is removed — its beta header is bound to the
// turn's context, which this request does not carry — and streaming,
// which a no-output request does not accept.
func (c *ClaudeClient) KeepPromptCacheWarm(ctx context.Context) (*models.UsageInfo, error) {
	if c == nil || c.provider.Mode() == auth.AuthModeOAuth {
		return nil, client.ErrPromptCacheKeepAliveUnsupported
	}
	c.lastRequestMu.Lock()
	last := c.lastRequest
	c.lastRequestMu.Unlock()
	if last == "" {
		return nil, client.ErrPromptCacheKeepAliveUnsupported
	}
	body, err := keepAliveBody([]byte(last))
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

// keepAliveBody derives the no-output request from a remembered body.
func keepAliveBody(last []byte) ([]byte, error) {
	var req map[string]interface{}
	if err := json.Unmarshal(last, &req); err != nil {
		return nil, fmt.Errorf("claudeai: keep-alive body: %w", err)
	}
	req["max_tokens"] = 0
	delete(req, "stream")
	if cfg, ok := req["output_config"].(map[string]interface{}); ok {
		delete(cfg, "task_budget")
		if len(cfg) == 0 {
			delete(req, "output_config")
		}
	}
	return json.Marshal(req)
}
