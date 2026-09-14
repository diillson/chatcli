/*
 * ChatCLI - OpenAI prompt-cache keep-alive
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package openai

import (
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

// KeepPromptCacheWarm re-sends the last request asking for one output
// token. OpenAI's prompt cache is automatic and prefix-keyed: any request
// that reads the prefix keeps it alive, and the cache is evicted after a
// few idle minutes, so a refresh during a long tool call costs the cached
// share of the prompt instead of the full re-tokenization the next turn
// would pay. One token is the smallest output the Chat Completions wire
// accepts; the usage comes back so the caller can book it.
func (c *OpenAIClient) KeepPromptCacheWarm(ctx context.Context) (*models.UsageInfo, error) {
	if c == nil {
		return nil, client.ErrPromptCacheKeepAliveUnsupported
	}
	last, ok := c.lastRequest.Take()
	if !ok {
		return nil, client.ErrPromptCacheKeepAliveUnsupported
	}
	body, err := client.KeepAliveRequestBody(last, 1)
	if err != nil {
		return nil, err
	}
	resp, err := auth.DoWithRefresh(ctx, c.provider, func(token string) (*http.Response, error) {
		return c.sendRequest(ctx, body, token)
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
	var result map[string]interface{}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("openai: keep-alive response: %w", err)
	}
	usage := client.ParseOpenAIUsage(result)
	if usage == nil {
		return nil, fmt.Errorf("openai: keep-alive response carried no usage")
	}
	c.logger.Debug("openai: prompt cache kept warm",
		zap.Int("cached", usage.CacheReadInputTokens), zap.String("model", c.model))
	return usage, nil
}
