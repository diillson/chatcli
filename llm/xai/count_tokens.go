/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * xAI token counting (POST /v1/tokenize-text): the endpoint tokenizes a
 * string and returns the tokens, so the count is the length of that array.
 *
 * It takes text rather than messages, so what is sent is the same
 * conversation the chat call would carry, flattened the way the chat body
 * flattens it. That makes the count an accurate measure of the prompt's
 * text and a slight under-count of the request, which adds a few
 * per-message framing tokens the endpoint never sees. The capability's
 * contract allows for that: it calibrates and reports, and no budget
 * decision depends on it.
 */
package xai

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

var _ client.TokenCounter = (*XAIClient)(nil)

// countTokensURL derives the tokenizer endpoint from the resolved chat
// completions URL, so a custom base URL is honored.
func (c *XAIClient) countTokensURL() string {
	return strings.TrimSuffix(strings.TrimSuffix(c.apiURL, "/"), "/chat/completions") + "/tokenize-text"
}

// countText flattens the conversation the way the tokenizer takes it: one
// string, roles included, because the role labels are billed too.
func countText(prompt string, history []models.Message) string {
	var b strings.Builder
	for _, msg := range history {
		role := strings.ToLower(strings.TrimSpace(msg.Role))
		if role == "" {
			role = "user"
		}
		b.WriteString(role)
		b.WriteString(": ")
		b.WriteString(msg.Content)
		b.WriteString("\n")
	}
	if p := strings.TrimSpace(prompt); p != "" &&
		(len(history) == 0 || history[len(history)-1].Role != "user" || history[len(history)-1].Content != prompt) {
		b.WriteString("user: ")
		b.WriteString(prompt)
	}
	return b.String()
}

// CountTokens implements client.TokenCounter.
func (c *XAIClient) CountTokens(ctx context.Context, prompt string, history []models.Message) (int, error) {
	text := countText(prompt, history)
	if strings.TrimSpace(text) == "" {
		return 0, nil
	}
	payload, err := json.Marshal(map[string]interface{}{"model": c.model, "text": text})
	if err != nil {
		return 0, err
	}
	opCtx, cancel := context.WithTimeout(ctx, countTokensTimeout)
	defer cancel()

	resp, err := auth.DoWithRefresh(opCtx, c.provider, func(token string) (*http.Response, error) {
		req, reqErr := http.NewRequestWithContext(opCtx, http.MethodPost, c.countTokensURL(), utils.NewJSONReader(payload))
		if reqErr != nil {
			return nil, reqErr
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		return c.client.Do(req)
	})
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return 0, err
	}
	if resp.StatusCode != http.StatusOK {
		return 0, &utils.APIError{StatusCode: resp.StatusCode, Message: utils.SanitizeSensitiveText(string(body))}
	}
	var out struct {
		TokenIDs []json.RawMessage `json:"token_ids"`
		Tokens   []json.RawMessage `json:"tokens"`
		Error    *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return 0, fmt.Errorf("tokenize-text: unreadable response: %w", err)
	}
	if out.Error != nil && out.Error.Message != "" {
		return 0, fmt.Errorf("tokenize-text: %s", utils.SanitizeSensitiveText(out.Error.Message))
	}
	// Both spellings are accepted: the count is the length either way, and
	// a rename of the array would otherwise read as a zero-token prompt.
	if n := len(out.TokenIDs); n > 0 {
		return n, nil
	}
	if n := len(out.Tokens); n > 0 {
		return n, nil
	}
	return 0, fmt.Errorf("tokenize-text: response carried no tokens")
}
