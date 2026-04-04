/*
 * ChatCLI - Native Tool Use support for MiniMax
 * MiniMax API é compatível com o formato OpenAI para tool calling.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package minimax

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/utils"
	"go.uber.org/zap"
)

// Ensure MiniMaxClient implements ToolAwareClient.
var _ client.ToolAwareClient = (*MiniMaxClient)(nil)

// SupportsNativeTools returns true — MiniMax supports native tool calling (OpenAI-compatible format).
func (c *MiniMaxClient) SupportsNativeTools() bool {
	return true
}

// SendPromptWithTools sends a prompt with tool definitions via MiniMax's native tool calling API.
func (c *MiniMaxClient) SendPromptWithTools(ctx context.Context, prompt string, history []models.Message, tools []models.ToolDefinition, maxTokens int) (*models.LLMResponse, error) {
	effectiveMaxTokens := maxTokens
	if effectiveMaxTokens <= 0 {
		effectiveMaxTokens = c.getMaxTokens()
	}

	// Sort tools alphabetically for KV cache stability
	sortedTools := client.SortToolDefinitions(tools)

	// Build messages with tool call support
	messages := buildToolMessages(prompt, history)

	// Build tools payload (OpenAI-compatible format)
	toolDefs := make([]map[string]interface{}, 0, len(sortedTools))
	for _, t := range sortedTools {
		toolDefs = append(toolDefs, map[string]interface{}{
			"type": "function",
			"function": map[string]interface{}{
				"name":        t.Function.Name,
				"description": t.Function.Description,
				"parameters":  t.Function.Parameters,
			},
		})
	}

	payload := map[string]interface{}{
		"model":      c.model,
		"messages":   messages,
		"max_tokens": effectiveMaxTokens,
	}
	if len(toolDefs) > 0 {
		payload["tools"] = toolDefs
	}

	jsonValue, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", i18n.T("llm.tool.error.marshaling_payload"), err)
	}

	resp, err := utils.Retry(ctx, c.logger, c.maxAttempts, c.backoff, func(ctx context.Context) (string, error) {
		httpResp, err := c.sendRequest(ctx, jsonValue)
		if err != nil {
			return "", err
		}
		defer httpResp.Body.Close()

		bodyBytes, err := io.ReadAll(httpResp.Body)
		if err != nil {
			return "", fmt.Errorf("%s: %w", i18n.T("llm.tool.error.reading_response"), err)
		}
		if httpResp.StatusCode != 200 {
			return "", &utils.APIError{StatusCode: httpResp.StatusCode, Message: utils.SanitizeSensitiveText(string(bodyBytes))}
		}
		return string(bodyBytes), nil
	})
	if err != nil {
		return nil, err
	}

	return parseToolResponse(resp, c.logger)
}

// buildToolMessages constructs the messages array supporting tool calls and results.
func buildToolMessages(prompt string, history []models.Message) []interface{} {
	var messages []interface{}

	for _, msg := range history {
		role := strings.ToLower(strings.TrimSpace(msg.Role))

		switch role {
		case "tool":
			// Tool result message
			messages = append(messages, map[string]interface{}{
				"role":         "tool",
				"tool_call_id": msg.ToolCallID,
				"content":      msg.Content,
			})
		case "assistant":
			if len(msg.ToolCalls) > 0 {
				// Assistant message with tool calls
				toolCalls := make([]map[string]interface{}, 0, len(msg.ToolCalls))
				for _, tc := range msg.ToolCalls {
					argsJSON := tc.ArgumentsJSON()
					toolCalls = append(toolCalls, map[string]interface{}{
						"id":   tc.ID,
						"type": "function",
						"function": map[string]interface{}{
							"name":      tc.Name,
							"arguments": argsJSON,
						},
					})
				}
				m := map[string]interface{}{
					"role":       "assistant",
					"tool_calls": toolCalls,
				}
				if msg.Content != "" {
					m["content"] = msg.Content
				}
				messages = append(messages, m)
			} else {
				messages = append(messages, map[string]interface{}{
					"role":    "assistant",
					"content": msg.Content,
				})
			}
		case "system", "user":
			messages = append(messages, map[string]interface{}{
				"role":    role,
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

// parseToolResponse parses the MiniMax API response with tool call support (OpenAI-compatible format).
func parseToolResponse(body string, logger *zap.Logger) (*models.LLMResponse, error) {
	var result map[string]interface{}
	if err := json.Unmarshal([]byte(body), &result); err != nil {
		return nil, fmt.Errorf("%s: %w", i18n.T("llm.tool.error.decoding_response"), err)
	}

	choices, ok := result["choices"].([]interface{})
	if !ok || len(choices) == 0 {
		return nil, fmt.Errorf("%s", i18n.T("llm.tool.error.no_choices"))
	}

	firstChoice, ok := choices[0].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("%s", i18n.T("llm.tool.error.unexpected_choice_format"))
	}

	message, ok := firstChoice["message"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("%s", i18n.T("llm.tool.error.no_message_in_choice"))
	}

	response := &models.LLMResponse{}

	// Extract content
	if content, ok := message["content"].(string); ok {
		response.Content = content
	}

	// Extract stop reason
	if reason, ok := firstChoice["finish_reason"].(string); ok {
		response.StopReason = reason
	}

	// Extract tool calls
	if toolCallsRaw, ok := message["tool_calls"].([]interface{}); ok {
		for _, tcRaw := range toolCallsRaw {
			tc, ok := tcRaw.(map[string]interface{})
			if !ok {
				continue
			}

			toolCall := models.ToolCall{
				Type: "function",
			}

			if id, ok := tc["id"].(string); ok {
				toolCall.ID = id
			}

			if fn, ok := tc["function"].(map[string]interface{}); ok {
				if name, ok := fn["name"].(string); ok {
					toolCall.Name = name
				}
				if argsStr, ok := fn["arguments"].(string); ok {
					var args map[string]interface{}
					if err := json.Unmarshal([]byte(argsStr), &args); err == nil {
						toolCall.Arguments = args
					} else {
						toolCall.Arguments = map[string]interface{}{"raw": argsStr}
					}
				}
			}

			response.ToolCalls = append(response.ToolCalls, toolCall)
		}
	}

	// Extract usage
	if usage, ok := result["usage"].(map[string]interface{}); ok {
		response.Usage = &models.UsageInfo{}
		if pt, ok := usage["prompt_tokens"].(float64); ok {
			response.Usage.PromptTokens = int(pt)
		}
		if ct, ok := usage["completion_tokens"].(float64); ok {
			response.Usage.CompletionTokens = int(ct)
		}
		if tt, ok := usage["total_tokens"].(float64); ok {
			response.Usage.TotalTokens = int(tt)
		}
	}

	return response, nil
}
