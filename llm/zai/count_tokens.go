/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Z.AI (GLM) token counting (POST /api/paas/v4/tokenizer): the same
 * messages the chat call would carry, answered as usage.prompt_tokens.
 * Free of charge; bounded here.
 */
package zai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/llm/internal/visionwire"
	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/utils"
)

const countTokensTimeout = 10 * time.Second

var _ client.TokenCounter = (*ZAIClient)(nil)

// countTokensURL derives the tokenizer endpoint from the resolved chat
// completions URL (the coding-plan endpoint included).
func (c *ZAIClient) countTokensURL() string {
	apiURL := ResolveAPIURL(c.apiURL)
	return strings.TrimSuffix(strings.TrimSuffix(apiURL, "/"), "/chat/completions") + "/tokenizer"
}

// countMessages mirrors SendPrompt's message shaping.
func countMessages(prompt string, history []models.Message) []map[string]interface{} {
	messages := make([]map[string]interface{}, 0, len(history)+1)
	for _, msg := range history {
		role := strings.ToLower(strings.TrimSpace(msg.Role))
		if role != "system" && role != "user" && role != "assistant" {
			role = "user"
		}
		messages = append(messages, map[string]interface{}{"role": role, "content": visionwire.OpenAIContent(msg.Content, msg.Images)})
	}
	if (len(history) == 0 || history[len(history)-1].Role != "user" || history[len(history)-1].Content != prompt) && strings.TrimSpace(prompt) != "" {
		messages = append(messages, map[string]interface{}{"role": "user", "content": prompt})
	}
	return messages
}

// CountTokens implements client.TokenCounter.
func (c *ZAIClient) CountTokens(ctx context.Context, prompt string, history []models.Message) (int, error) {
	messages := countMessages(prompt, history)
	if len(messages) == 0 {
		return 0, nil
	}
	payload, err := json.Marshal(map[string]interface{}{"model": c.model, "messages": messages})
	if err != nil {
		return 0, err
	}
	opCtx, cancel := context.WithTimeout(ctx, countTokensTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(opCtx, http.MethodPost, c.countTokensURL(), utils.NewJSONReader(payload))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.getAuthToken(opCtx))
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
		Usage struct {
			PromptTokens int `json:"prompt_tokens"`
			TotalTokens  int `json:"total_tokens"`
		} `json:"usage"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return 0, fmt.Errorf("tokenizer: unreadable response: %w", err)
	}
	if out.Error != nil && out.Error.Message != "" {
		return 0, fmt.Errorf("tokenizer: %s", utils.SanitizeSensitiveText(out.Error.Message))
	}
	if out.Usage.PromptTokens > 0 {
		return out.Usage.PromptTokens, nil
	}
	return out.Usage.TotalTokens, nil
}
