/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Moonshot/Kimi token counting (POST /v1/tokenizers/estimate-token-count):
 * the same messages the chat call would carry, answered as
 * data.total_tokens. Free of charge; bounded here.
 */
package moonshot

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/utils"
)

const countTokensTimeout = 10 * time.Second

var _ client.TokenCounter = (*MoonshotClient)(nil)

// countTokensURL derives the tokenizer endpoint from the chat completions
// URL (MOONSHOT_API_URL honored the same way sendRequest does).
func (c *MoonshotClient) countTokensURL() string {
	apiURL := utils.GetEnvOrDefault("MOONSHOT_API_URL", c.apiURL)
	return strings.TrimSuffix(strings.TrimSuffix(apiURL, "/"), "/chat/completions") + "/tokenizers/estimate-token-count"
}

// CountTokens implements client.TokenCounter.
func (c *MoonshotClient) CountTokens(ctx context.Context, prompt string, history []models.Message) (int, error) {
	messages := buildToolMessages(prompt, history)
	if len(messages) == 0 {
		return 0, nil
	}
	payload, err := json.Marshal(map[string]interface{}{"model": c.model, "messages": messages})
	if err != nil {
		return 0, err
	}
	opCtx, cancel := context.WithTimeout(ctx, countTokensTimeout)
	defer cancel()
	token, err := c.provider.Token(opCtx)
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(opCtx, http.MethodPost, c.countTokensURL(), utils.NewJSONReader(payload))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
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
		Data struct {
			TotalTokens int `json:"total_tokens"`
		} `json:"data"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return 0, fmt.Errorf("estimate-token-count: unreadable response: %w", err)
	}
	if out.Error != nil && out.Error.Message != "" {
		return 0, fmt.Errorf("estimate-token-count: %s", utils.SanitizeSensitiveText(out.Error.Message))
	}
	return out.Data.TotalTokens, nil
}
