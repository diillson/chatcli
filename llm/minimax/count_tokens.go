/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * MiniMax token counting (POST /v1/responses/input_tokens): the endpoint
 * takes the same request the model would answer and returns how many input
 * tokens it counts, without running it.
 *
 * Only on the native surface. With MINIMAX_API_COMPAT set the client
 * speaks another vendor's schema against another base URL, and this
 * endpoint is not part of that contract — the capability reports absent
 * there rather than guessing.
 */
package minimax

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

var _ client.TokenCounter = (*MiniMaxClient)(nil)

// countTokensURL derives the counting endpoint from the resolved chat
// URL, so a custom base URL is honored.
func (c *MiniMaxClient) countTokensURL() string {
	base := strings.TrimSuffix(strings.TrimSuffix(c.apiURL, "/"), "/text/chatcompletion_v2")
	base = strings.TrimSuffix(strings.TrimSuffix(base, "/"), "/chat/completions")
	return base + "/responses/input_tokens"
}

// countInput shapes the conversation the way the endpoint takes it: the
// same role/content items the Responses schema carries.
func countInput(prompt string, history []models.Message) []map[string]string {
	items := make([]map[string]string, 0, len(history)+1)
	for _, msg := range history {
		role := strings.ToLower(strings.TrimSpace(msg.Role))
		if role != "system" && role != "user" && role != "assistant" {
			role = "user"
		}
		items = append(items, map[string]string{"role": role, "content": msg.Content})
	}
	if p := strings.TrimSpace(prompt); p != "" &&
		(len(history) == 0 || history[len(history)-1].Role != "user" || history[len(history)-1].Content != prompt) {
		items = append(items, map[string]string{"role": "user", "content": prompt})
	}
	return items
}

// CountTokens implements client.TokenCounter.
func (c *MiniMaxClient) CountTokens(ctx context.Context, prompt string, history []models.Message) (int, error) {
	if c.anthropicCompat {
		return 0, fmt.Errorf("input_tokens: not served on the compatibility surface")
	}
	items := countInput(prompt, history)
	if len(items) == 0 {
		return 0, nil
	}
	payload, err := json.Marshal(map[string]interface{}{"model": c.model, "input": items})
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

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if err != nil {
		return 0, err
	}
	if resp.StatusCode != http.StatusOK {
		return 0, &utils.APIError{StatusCode: resp.StatusCode, Message: utils.SanitizeSensitiveText(string(body))}
	}
	var out struct {
		InputTokens int `json:"input_tokens"`
		BaseResp    *struct {
			StatusCode int    `json:"status_code"`
			StatusMsg  string `json:"status_msg"`
		} `json:"base_resp"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return 0, fmt.Errorf("input_tokens: unreadable response: %w", err)
	}
	// MiniMax reports application errors in base_resp with HTTP 200.
	if out.BaseResp != nil && out.BaseResp.StatusCode != 0 {
		return 0, fmt.Errorf("input_tokens: %s", utils.SanitizeSensitiveText(out.BaseResp.StatusMsg))
	}
	if out.InputTokens <= 0 {
		return 0, fmt.Errorf("input_tokens: response carried no count")
	}
	return out.InputTokens, nil
}
