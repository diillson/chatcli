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
	"time"

	"github.com/diillson/chatcli/auth"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/llm/internal/visionwire"
	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/utils"
	"go.uber.org/zap"
)

// Ensure ClaudeClient implements ToolAwareClient.
var _ client.ToolAwareClient = (*ClaudeClient)(nil)
var _ client.ThinkingAwareClient = (*ClaudeClient)(nil)

// SupportsNativeTools returns true for API key auth, false for OAuth.
// The OAuth endpoint (console.anthropic.com) uses a different message format
// and requires streaming — tool calling via native API is not compatible.
// OAuth users fall back to XML-based tool parsing which works reliably.
func (c *ClaudeClient) SupportsNativeTools() bool {
	return c.provider.Mode() != auth.AuthModeOAuth
}

// SendPromptWithTools sends a prompt with tool definitions via Anthropic's native tool use API.
func (c *ClaudeClient) SendPromptWithTools(ctx context.Context, prompt string, history []models.Message, tools []models.ToolDefinition, maxTokens int) (*models.LLMResponse, error) {
	// Clear per-instance usage so a call that reports none falls back to
	// estimation instead of re-counting the previous call's tokens.
	c.resetUsage()

	effectiveMaxTokens := maxTokens
	if effectiveMaxTokens <= 0 {
		effectiveMaxTokens = c.getMaxTokens()
	}

	// Sort tools for KV cache stability
	sortedTools := client.SortToolDefinitions(tools)

	// Build system prompt with cache control. The tool path reserves 1
	// breakpoint for the tool definitions block below, so system markers
	// are capped at anthropicMaxCacheBreakpoints-1.
	systemBlocks := coalesceCacheControl(buildSystemBlocks(history), anthropicMaxCacheBreakpoints-1)

	// Build messages (excluding system messages)
	messages := buildClaudeToolMessages(prompt, history)

	// Build tool definitions for Anthropic format.
	// Mark the LAST tool with cache_control: ephemeral so the whole tool
	// block becomes part of the Anthropic KV cache — identical tool
	// definitions across turns are served as a cache read instead of a
	// full re-tokenization (saves tens of thousands of tokens per turn
	// in agent mode where 15-17 coder+plugin tools are re-sent).
	toolDefs := make([]map[string]interface{}, 0, len(sortedTools))
	for i, t := range sortedTools {
		toolDef := map[string]interface{}{
			"name":         t.Function.Name,
			"description":  t.Function.Description,
			"input_schema": t.Function.Parameters,
		}
		if i == len(sortedTools)-1 {
			toolDef["cache_control"] = client.AnthropicCacheMarker()
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
	// Provider context engine: let Anthropic clear stale tool results
	// server-side (context editing beta) on top of the local compaction.
	if cm := client.AnthropicContextManagement(); cm != nil && len(toolDefs) > 0 {
		reqBody["context_management"] = cm
	}

	// Skill effort hint → thinking (tool-use path). Opus 4.7+ uses adaptive,
	// older models use budgeted extended thinking — see applyThinkingForEffort.
	applyThinkingForEffort(reqBody, c.model, ctx)

	// Opus 4.8 fast mode opt-in via ANTHROPIC_SPEED=fast.
	applyFastModeIfRequested(reqBody, c.model)

	// Rolling conversation breakpoint on the last user/tool_result message —
	// in a tool loop this is the bulk of every request and was re-tokenized
	// at full price each turn. Before the budget pass so the LATEST markers
	// survive when the cap is exceeded.
	client.MarkAnthropicHistoryBreakpoint(client.AnthropicMessages{List: messages}, client.AnthropicCacheMarker())
	enforceCacheControlBudget(reqBody, anthropicMaxCacheBreakpoints)

	jsonValue, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", i18n.T("llm.tool.error.marshaling_payload"), err)
	}

	start := time.Now()
	client.LogRequestStart(c.logger, "CLAUDEAI", c.model,
		zap.String("path", "tool_use"),
		zap.Int("payload_bytes", len(jsonValue)),
		zap.Int("history_len", len(history)),
		zap.Int("tool_count", len(sortedTools)),
		zap.Int("max_tokens", effectiveMaxTokens),
		zap.Int("cache_markers", client.CountAnthropicCacheMarkers(reqBody)),
	)

	respBody, err := utils.Retry(ctx, c.logger, c.maxAttempts, c.backoff, func(ctx context.Context) (string, error) {
		resp, err := auth.DoWithRefresh(ctx, c.provider, func(token string) (*http.Response, error) {
			req, err := c.buildToolRequest(ctx, jsonValue, token)
			if err != nil {
				return nil, err
			}
			return c.client.Do(req)
		})
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()

		// Decode compressed response (gzip, deflate, br) — OAuth endpoints
		// send compressed responses when Accept-Encoding is set.
		reader, decErr := decodeResponseBody(resp)
		if decErr != nil {
			return "", fmt.Errorf("%s: %w", i18n.T("llm.tool.error.decoding_response_body"), decErr)
		}
		if reader != resp.Body {
			defer reader.Close()
		}

		bodyBytes, err := io.ReadAll(reader)
		if err != nil {
			return "", fmt.Errorf("%s: %w", i18n.T("llm.tool.error.reading_response"), err)
		}

		if resp.StatusCode != 200 {
			return "", &utils.APIError{StatusCode: resp.StatusCode, Message: utils.SanitizeSensitiveText(string(bodyBytes))}
		}
		return string(bodyBytes), nil
	})
	if err != nil {
		client.LogRequestFinish(c.logger, "CLAUDEAI", c.model, "error", time.Since(start),
			zap.String("path", "tool_use"),
		)
		return nil, err
	}
	client.LogRequestFinish(c.logger, "CLAUDEAI", c.model, "success", time.Since(start),
		zap.String("path", "tool_use"),
		zap.Int("response_bytes", len(respBody)),
	)

	response, err := parseClaudeToolResponse(respBody, c.logger)
	if err == nil && response != nil {
		// Mirror the reasoning blocks onto the instance too: callers that
		// hold the response replay from it, callers on the plain path read
		// them back through LastThinking.
		c.storeThinking(response.Thinking)
	}
	if err == nil && response != nil && response.Usage != nil {
		// Per-instance mirror of what parseClaudeToolResponse recorded in the
		// legacy global — parallel clients must not cross-attribute tokens.
		c.usage.StoreUsage(response.Usage)
		if response.StopReason != "" {
			c.usage.StoreStopReason(response.StopReason)
		}
	}
	return response, err
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
					block["cache_control"] = client.AnthropicCacheMarker()
				}
				blocks = append(blocks, block)
			}
		} else {
			// Single system message — mark as ephemeral for KV cache reuse
			blocks = append(blocks, map[string]interface{}{
				"type":          "text",
				"text":          msg.Content,
				"cache_control": client.AnthropicCacheMarker(),
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
			// Tool results in Anthropic format: user message with a
			// tool_result content block. When the agent layer marked the
			// result as IsError, we forward Anthropic's native is_error
			// field so the model can reason about retryability without
			// parsing English. ErrorCode is carried as a structured
			// marker prefix inside content (Anthropic doesn't have a
			// dedicated error_code wire field, but the model picks up
			// the [ERROR:<code>] tag reliably as part of caching too).
			toolResultBlock := map[string]interface{}{
				"type":        "tool_result",
				"tool_use_id": msg.ToolCallID,
				"content":     msg.Content,
			}
			if msg.IsError {
				toolResultBlock["is_error"] = true
				if msg.ErrorCode != "" {
					// Prepend a stable marker so the model can detect
					// the error class even if it ignores is_error.
					toolResultBlock["content"] = "[ERROR:" + msg.ErrorCode + "] " + msg.Content
				}
			}
			messages = append(messages, map[string]interface{}{
				"role":    "user",
				"content": []map[string]interface{}{toolResultBlock},
			})

		case "assistant":
			if len(msg.ToolCalls) > 0 {
				// Assistant with tool_use content blocks. Reasoning blocks
				// come first when the turn has any: with extended thinking
				// on, the provider expects the thinking that produced the
				// tool call to travel back inside the same turn, ahead of
				// the text and the tool_use blocks.
				var content []interface{}
				for _, blk := range client.AnthropicThinkingBlocks(msg.Thinking).Blocks {
					content = append(content, blk)
				}
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
				"content": visionwire.AnthropicContent(msg.Content, msg.Images),
			})

		default:
			messages = append(messages, map[string]interface{}{
				"role":    "user",
				"content": visionwire.AnthropicContent(msg.Content, msg.Images),
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

// buildToolRequest creates the HTTP request for tool use calls. The token
// argument is the raw access token (no prefix); the provider's mode picks
// the auth header style.
func (c *ClaudeClient) buildToolRequest(ctx context.Context, jsonValue []byte, token string) (*http.Request, error) {
	reqURL := c.apiURL
	if c.provider.Mode() == auth.AuthModeOAuth {
		reqURL = withBetaQuery(reqURL)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(jsonValue))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", i18n.T("llm.tool.error.creating_request"), err)
	}

	req.Header.Set("Content-Type", "application/json")
	c.applyAuthHeaders(req, token)
	return req, nil
}

// parseAppliedEdits sums the applied_edits entries of a context_management
// response block ({type, cleared_tool_uses, cleared_input_tokens}).
func parseAppliedEdits(edits []interface{}) *models.ContextEdits {
	out := &models.ContextEdits{}
	for _, raw := range edits {
		e, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		if v, ok := e["cleared_tool_uses"].(float64); ok {
			out.ClearedToolUses += int(v)
		}
		if v, ok := e["cleared_input_tokens"].(float64); ok {
			out.ClearedInputTokens += int(v)
		}
	}
	return out
}

// parseClaudeToolResponse parses the Anthropic API response with tool use support.
func parseClaudeToolResponse(body string, logger *zap.Logger) (*models.LLMResponse, error) {
	var result map[string]interface{}
	if err := json.Unmarshal([]byte(body), &result); err != nil {
		return nil, fmt.Errorf("%s: %w", i18n.T("llm.tool.error.decoding_response"), err)
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
		case "thinking", "redacted_thinking":
			// Collected below in one pass so the blocks keep their arrival
			// order; the provider requires them replayed unchanged.
			continue
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
	response.Thinking = client.ParseAnthropicThinkingBody([]byte(body))

	// Server-side context edits applied by the provider context engine:
	// parsed into the response so the caller can mirror them locally
	// (stub the cleared results, skip calibration, note the rebuild).
	if cm, ok := result["context_management"].(map[string]interface{}); ok {
		if edits, ok := cm["applied_edits"].([]interface{}); ok && len(edits) > 0 {
			response.ContextEdits = parseAppliedEdits(edits)
			if logger != nil {
				logger.Info("anthropic context editing applied", zap.Int("edits", len(edits)),
					zap.Int("cleared_tool_uses", response.ContextEdits.ClearedToolUses),
					zap.Int("cleared_input_tokens", response.ContextEdits.ClearedInputTokens))
			}
		}
	}

	// Extract usage
	if _, ok := result["usage"].(map[string]interface{}); ok {
		// One parser for both surfaces: the 1-hour cache-write share
		// (cache_creation.ephemeral_1h_input_tokens) is billed at 2x and
		// was dropped here, under-pricing coder sessions on the hour TTL.
		response.Usage = client.ParseAnthropicUsage(result)
		// Store in global tracker so LastUsage() works
		RecordClaudeUsage(response.Usage)
	}

	// Extract stop_reason
	if sr, ok := result["stop_reason"].(string); ok && sr != "" {
		RecordClaudeStopReason(sr)
	}

	return response, nil
}
