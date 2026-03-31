/*
 * ChatCLI - Native Tool Use support for Claude (Anthropic)
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
	"strings"

	"github.com/diillson/chatcli/llm/catalog"
	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/utils"
	"go.uber.org/zap"
)

// Ensure ClaudeClient implements ToolAwareClient.
var _ client.ToolAwareClient = (*ClaudeClient)(nil)

// SupportsNativeTools returns true — Claude supports native tool calling.
func (c *ClaudeClient) SupportsNativeTools() bool {
	return true
}

// SendPromptWithTools sends a prompt with tool definitions via Anthropic's native tool use API.
func (c *ClaudeClient) SendPromptWithTools(ctx context.Context, prompt string, history []models.Message, tools []models.ToolDefinition, maxTokens int) (*models.LLMResponse, error) {
	effectiveMaxTokens := maxTokens
	if effectiveMaxTokens <= 0 {
		effectiveMaxTokens = c.getMaxTokens()
	}

	// Sort tools for KV cache stability
	sortedTools := client.SortToolDefinitions(tools)

	// Build system prompt with cache control
	systemBlocks := buildSystemBlocks(history)

	// Build messages (excluding system messages)
	messages := buildClaudeToolMessages(prompt, history)

	// Build tool definitions for Anthropic format
	toolDefs := make([]map[string]interface{}, 0, len(sortedTools))
	for _, t := range sortedTools {
		toolDef := map[string]interface{}{
			"name":         t.Function.Name,
			"description":  t.Function.Description,
			"input_schema": t.Function.Parameters,
		}
		toolDefs = append(toolDefs, toolDef)
	}

	reqBody := map[string]interface{}{
		"model":      c.model,
		"max_tokens": effectiveMaxTokens,
		"messages":   messages,
	}

	if len(systemBlocks) > 0 {
		reqBody["system"] = systemBlocks
	}
	if len(toolDefs) > 0 {
		reqBody["tools"] = toolDefs
	}

	jsonValue, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshaling payload: %w", err)
	}

	respBody, err := utils.Retry(ctx, c.logger, c.maxAttempts, c.backoff, func(ctx context.Context) (string, error) {
		req, err := c.buildToolRequest(ctx, jsonValue)
		if err != nil {
			return "", err
		}

		resp, err := c.client.Do(req)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()

		// Decode compressed response (gzip, deflate, br) — OAuth endpoints
		// send compressed responses when Accept-Encoding is set.
		reader, decErr := decodeResponseBody(resp)
		if decErr != nil {
			return "", fmt.Errorf("decoding response body: %w", decErr)
		}
		if reader != resp.Body {
			defer reader.Close()
		}

		bodyBytes, err := io.ReadAll(reader)
		if err != nil {
			return "", fmt.Errorf("reading response: %w", err)
		}

		if resp.StatusCode != 200 {
			return "", &utils.APIError{StatusCode: resp.StatusCode, Message: utils.SanitizeSensitiveText(string(bodyBytes))}
		}
		return string(bodyBytes), nil
	})
	if err != nil {
		return nil, err
	}

	return parseClaudeToolResponse(respBody, c.logger)
}

// buildSystemBlocks creates system prompt blocks with cache_control:ephemeral for KV cache reuse.
func buildSystemBlocks(history []models.Message) []map[string]interface{} {
	var blocks []map[string]interface{}

	for _, msg := range history {
		if strings.ToLower(msg.Role) != "system" {
			continue
		}

		// If structured system parts are available, use them
		if len(msg.SystemParts) > 0 {
			for _, part := range msg.SystemParts {
				block := map[string]interface{}{
					"type": "text",
					"text": part.Text,
				}
				if part.CacheControl != nil {
					block["cache_control"] = map[string]string{
						"type": part.CacheControl.Type,
					}
				}
				blocks = append(blocks, block)
			}
		} else {
			// Single system message — mark as ephemeral for KV cache reuse
			blocks = append(blocks, map[string]interface{}{
				"type": "text",
				"text": msg.Content,
				"cache_control": map[string]string{
					"type": "ephemeral",
				},
			})
		}
	}

	return blocks
}

// buildClaudeToolMessages constructs messages for Claude's tool use format.
func buildClaudeToolMessages(prompt string, history []models.Message) []interface{} {
	var messages []interface{}

	for _, msg := range history {
		role := strings.ToLower(strings.TrimSpace(msg.Role))

		switch role {
		case "system":
			continue // Handled separately via system blocks

		case "tool":
			// Tool results in Anthropic format: user message with tool_result content block
			messages = append(messages, map[string]interface{}{
				"role": "user",
				"content": []map[string]interface{}{
					{
						"type":        "tool_result",
						"tool_use_id": msg.ToolCallID,
						"content":     msg.Content,
					},
				},
			})

		case "assistant":
			if len(msg.ToolCalls) > 0 {
				// Assistant with tool_use content blocks
				var content []interface{}
				if msg.Content != "" {
					content = append(content, map[string]interface{}{
						"type": "text",
						"text": msg.Content,
					})
				}
				for _, tc := range msg.ToolCalls {
					content = append(content, map[string]interface{}{
						"type":  "tool_use",
						"id":    tc.ID,
						"name":  tc.Name,
						"input": tc.Arguments,
					})
				}
				messages = append(messages, map[string]interface{}{
					"role":    "assistant",
					"content": content,
				})
			} else {
				messages = append(messages, map[string]interface{}{
					"role":    "assistant",
					"content": msg.Content,
				})
			}

		case "user":
			messages = append(messages, map[string]interface{}{
				"role":    "user",
				"content": msg.Content,
			})

		default:
			messages = append(messages, map[string]interface{}{
				"role":    "user",
				"content": msg.Content,
			})
		}
	}

	// Add prompt as user message if needed
	if len(history) == 0 || history[len(history)-1].Role != "user" || history[len(history)-1].Content != prompt {
		if strings.TrimSpace(prompt) != "" {
			messages = append(messages, map[string]interface{}{
				"role":    "user",
				"content": prompt,
			})
		}
	}

	return messages
}

// buildToolRequest creates the HTTP request for tool use calls.
func (c *ClaudeClient) buildToolRequest(ctx context.Context, jsonValue []byte) (*http.Request, error) {
	reqURL := c.apiURL
	if strings.HasPrefix(c.apiKey, "oauth:") {
		reqURL = withBetaQuery(reqURL)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(jsonValue))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	version := catalog.GetAnthropicAPIVersion(c.model)
	if version == "" {
		version = "2023-06-01"
	}
	req.Header.Set("anthropic-version", version)

	if strings.HasPrefix(c.apiKey, "oauth:") {
		applyOAuthHeaders(req, c.apiKey)
	} else if strings.HasPrefix(c.apiKey, "token:") {
		req.Header.Set("Authorization", "Bearer "+strings.TrimPrefix(c.apiKey, "token:"))
	} else if strings.HasPrefix(c.apiKey, "apikey:") {
		req.Header.Set("x-api-key", strings.TrimPrefix(c.apiKey, "apikey:"))
	} else {
		req.Header.Set("x-api-key", c.apiKey)
	}

	return req, nil
}

// parseClaudeToolResponse parses the Anthropic API response with tool use support.
func parseClaudeToolResponse(body string, logger *zap.Logger) (*models.LLMResponse, error) {
	var result map[string]interface{}
	if err := json.Unmarshal([]byte(body), &result); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}

	response := &models.LLMResponse{}

	// Extract stop reason
	if reason, ok := result["stop_reason"].(string); ok {
		response.StopReason = reason
	}

	// Parse content blocks
	contentBlocks, ok := result["content"].([]interface{})
	if !ok {
		return response, nil
	}

	var textParts []string
	for _, block := range contentBlocks {
		b, ok := block.(map[string]interface{})
		if !ok {
			continue
		}

		blockType, _ := b["type"].(string)
		switch blockType {
		case "text":
			if text, ok := b["text"].(string); ok {
				textParts = append(textParts, text)
			}
		case "tool_use":
			tc := models.ToolCall{Type: "function"}
			if id, ok := b["id"].(string); ok {
				tc.ID = id
			}
			if name, ok := b["name"].(string); ok {
				tc.Name = name
			}
			if input, ok := b["input"].(map[string]interface{}); ok {
				tc.Arguments = input
			}
			response.ToolCalls = append(response.ToolCalls, tc)
		}
	}

	response.Content = strings.Join(textParts, "\n")

	// Extract usage
	if usage, ok := result["usage"].(map[string]interface{}); ok {
		response.Usage = &models.UsageInfo{}
		if it, ok := usage["input_tokens"].(float64); ok {
			response.Usage.PromptTokens = int(it)
		}
		if ot, ok := usage["output_tokens"].(float64); ok {
			response.Usage.CompletionTokens = int(ot)
		}
		response.Usage.TotalTokens = response.Usage.PromptTokens + response.Usage.CompletionTokens
	}

	return response, nil
}
