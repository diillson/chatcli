/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Gemini token counting (POST models/{model}:countTokens). The request is
 * the generateContentRequest the adapter would send — contents and the
 * inline system instruction — so the count is the size the next
 * generateContent call is billed for. Free of charge; bounded here.
 */
package googleai

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

var _ client.TokenCounter = (*GeminiClient)(nil)

// CountTokens implements client.TokenCounter.
func (c *GeminiClient) CountTokens(ctx context.Context, prompt string, history []models.Message) (int, error) {
	contents, systemInstruction, _ := c.buildContentsAndSystem(history, prompt)
	if len(contents) == 0 && strings.TrimSpace(prompt) != "" {
		contents = []map[string]interface{}{{"role": "user", "parts": []map[string]string{{"text": prompt}}}}
	}
	if len(contents) == 0 {
		return 0, nil
	}
	genReq := map[string]interface{}{
		"model":    "models/" + c.model,
		"contents": contents,
	}
	if systemInstruction != nil {
		genReq["system_instruction"] = systemInstruction
	}
	payload, err := json.Marshal(map[string]interface{}{"generateContentRequest": genReq})
	if err != nil {
		return 0, err
	}
	opCtx, cancel := context.WithTimeout(ctx, countTokensTimeout)
	defer cancel()
	token, err := c.provider.Token(opCtx)
	if err != nil {
		return 0, err
	}
	url := fmt.Sprintf("%s/models/%s:countTokens", c.baseURL, c.model)
	req, err := http.NewRequestWithContext(opCtx, http.MethodPost, url, strings.NewReader(string(payload)))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", token)
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
		TotalTokens int `json:"totalTokens"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return 0, fmt.Errorf("countTokens: unreadable response: %w", err)
	}
	return out.TotalTokens, nil
}
