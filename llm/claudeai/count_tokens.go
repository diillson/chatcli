/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Anthropic token counting (POST /v1/messages/count_tokens). The request
 * carries exactly what SendPrompt would send — same message shaping, same
 * system blocks, same auth mode headers — minus the generation-only
 * fields (max_tokens, stream, thinking), so the count is the size the
 * next call will be billed for (cache reads and writes included). Free of
 * charge on Anthropic's side; bounded here so a slow count never stalls
 * the caller.
 */
package claudeai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/diillson/chatcli/auth"
	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/utils"
)

const countTokensTimeout = 10 * time.Second

// Compile-time proof the client exposes the optional capability.
var _ client.TokenCounter = (*ClaudeClient)(nil)

// CountTokens implements client.TokenCounter.
func (c *ClaudeClient) CountTokens(ctx context.Context, prompt string, history []models.Message) (int, error) {
	isOAuth := c.provider.Mode() == auth.AuthModeOAuth
	var messages []map[string]interface{}
	var systemObj interface{}
	if isOAuth {
		messages, systemObj = c.buildOAuthMessagesAndSystem(prompt, history)
	} else {
		messages, systemObj = c.buildMessagesAndSystem(prompt, history)
	}
	if len(messages) == 0 {
		return 0, nil
	}
	reqBody := map[string]interface{}{
		"model":    c.model,
		"messages": messages,
	}
	if systemObj != nil {
		reqBody["system"] = systemObj
	}
	client.MarkAnthropicHistoryBreakpoint(client.AnthropicMessages{Maps: messages}, client.AnthropicCacheMarker())
	enforceCacheControlBudget(reqBody, anthropicMaxCacheBreakpoints)

	payload, err := json.Marshal(reqBody)
	if err != nil {
		return 0, err
	}
	opCtx, cancel := context.WithTimeout(ctx, countTokensTimeout)
	defer cancel()
	token, err := c.provider.Token(opCtx)
	if err != nil {
		return 0, err
	}
	url := strings.TrimSuffix(c.apiURL, "/") + "/count_tokens"
	reqCtx := context.WithValue(opCtx, oauthModelKey{}, c.model)
	if isOAuth {
		url = withBetaQuery(url)
	}
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, url, strings.NewReader(string(payload)))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	c.applyAuthHeaders(req, token)

	resp, err := c.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if err != nil {
		return 0, err
	}
	if resp.StatusCode != http.StatusOK {
		return 0, &utils.APIError{StatusCode: resp.StatusCode, Message: utils.SanitizeSensitiveText(string(body))}
	}
	var out struct {
		InputTokens int `json:"input_tokens"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return 0, fmt.Errorf("count_tokens: unreadable response: %w", err)
	}
	return out.InputTokens, nil
}
