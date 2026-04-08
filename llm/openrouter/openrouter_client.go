/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package openrouter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/diillson/chatcli/auth"
	"github.com/diillson/chatcli/config"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/llm/catalog"
	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/utils"
	"go.uber.org/zap"
)

// OpenRouterClient implements the LLM client for the OpenRouter API.
// OpenRouter is an OpenAI-compatible gateway that routes requests to
// multiple LLM providers (OpenAI, Anthropic, Google, Meta, etc.).
type OpenRouterClient struct {
	apiKey      string
	model       string
	logger      *zap.Logger
	client      *http.Client
	maxAttempts int
	backoff     time.Duration
}

// NewOpenRouterClient creates a new OpenRouter client instance.
func NewOpenRouterClient(apiKey, model string, logger *zap.Logger, maxAttempts int, backoff time.Duration) *OpenRouterClient {
	httpClient := utils.NewHTTPClient(logger, 900*time.Second)

	return &OpenRouterClient{
		apiKey:      apiKey,
		model:       model,
		logger:      logger,
		client:      httpClient,
		maxAttempts: maxAttempts,
		backoff:     backoff,
	}
}

// GetModelName returns the display name for the current model via catalog.
func (c *OpenRouterClient) GetModelName() string {
	return catalog.GetDisplayName(catalog.ProviderOpenRouter, c.model)
}

// getMaxTokens resolves max tokens from env override, catalog, or fallback.
func (c *OpenRouterClient) getMaxTokens() int {
	if tokenStr := os.Getenv("OPENROUTER_MAX_TOKENS"); tokenStr != "" {
		if parsedTokens, err := strconv.Atoi(tokenStr); err == nil && parsedTokens > 0 {
			c.logger.Debug(i18n.T("llm.info.using_custom_max_tokens", "OPENROUTER_MAX_TOKENS"),
				zap.Int("max_tokens", parsedTokens))
			return parsedTokens
		}
	}
	return catalog.GetMaxTokens(catalog.ProviderOpenRouter, c.model, 0)
}

// getAPIURL resolves the OpenRouter API URL from env override or default.
func (c *OpenRouterClient) getAPIURL() string {
	return utils.GetEnvOrDefault("OPENROUTER_API_URL", config.OpenRouterAPIURL)
}

// buildMessages converts the conversation history into the OpenAI-compatible
// messages format used by OpenRouter.
func (c *OpenRouterClient) buildMessages(prompt string, history []models.Message) []map[string]interface{} {
	messages := make([]map[string]interface{}, 0, len(history)+1)

	for _, msg := range history {
		role := strings.ToLower(strings.TrimSpace(msg.Role))
		switch role {
		case "system", "user", "assistant", "tool":
			// valid roles
		default:
			role = "user"
		}

		msgMap := map[string]interface{}{
			"role":    role,
			"content": msg.Content,
		}

		// Preserve tool_call_id for tool response messages
		if role == "tool" && msg.ToolCallID != "" {
			msgMap["tool_call_id"] = msg.ToolCallID
		}

		messages = append(messages, msgMap)
	}

	// Fallback: if history doesn't contain the last user prompt, add it
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

// buildPayload assembles the request payload with OpenRouter-specific options.
func (c *OpenRouterClient) buildPayload(messages []map[string]interface{}, maxTokens int) map[string]interface{} {
	payload := map[string]interface{}{
		"model":      c.model,
		"messages":   messages,
		"max_tokens": maxTokens,
	}

	// OpenRouter-specific: fallback models (try primary, then fallbacks)
	if fallbackModels := os.Getenv("OPENROUTER_FALLBACK_MODELS"); fallbackModels != "" {
		modelList := strings.Split(fallbackModels, ",")
		trimmed := make([]string, 0, len(modelList))
		for _, m := range modelList {
			if s := strings.TrimSpace(m); s != "" {
				trimmed = append(trimmed, s)
			}
		}
		if len(trimmed) > 0 {
			// OpenRouter "models" field: first is primary, rest are fallbacks
			allModels := append([]string{c.model}, trimmed...)
			payload["models"] = allModels
			payload["route"] = "fallback"
		}
	}

	// OpenRouter-specific: provider ordering preference
	if providerOrder := os.Getenv("OPENROUTER_PROVIDER_ORDER"); providerOrder != "" {
		orderList := strings.Split(providerOrder, ",")
		trimmed := make([]string, 0, len(orderList))
		for _, p := range orderList {
			if s := strings.TrimSpace(p); s != "" {
				trimmed = append(trimmed, s)
			}
		}
		if len(trimmed) > 0 {
			payload["provider"] = map[string]interface{}{
				"order": trimmed,
			}
		}
	}

	// OpenRouter-specific: message transforms (e.g., "middle-out" for context overflow)
	if transforms := os.Getenv("OPENROUTER_TRANSFORMS"); transforms != "" {
		transformList := strings.Split(transforms, ",")
		trimmed := make([]string, 0, len(transformList))
		for _, t := range transformList {
			if s := strings.TrimSpace(t); s != "" {
				trimmed = append(trimmed, s)
			}
		}
		if len(trimmed) > 0 {
			payload["transforms"] = trimmed
		}
	}

	// Tool calling support: inject tools from history metadata
	if tools := c.resolveTools(); tools != nil {
		payload["tools"] = tools
	}

	return payload
}

// resolveTools checks for tool definitions in the OPENROUTER_TOOLS env var.
// Format: JSON array of OpenAI-compatible tool definitions.
func (c *OpenRouterClient) resolveTools() []interface{} {
	toolsJSON := os.Getenv("OPENROUTER_TOOLS")
	if toolsJSON == "" {
		return nil
	}

	var tools []interface{}
	if err := json.Unmarshal([]byte(toolsJSON), &tools); err != nil {
		c.logger.Warn("Failed to parse OPENROUTER_TOOLS",
			zap.Error(err))
		return nil
	}
	return tools
}

// SendPrompt sends a prompt to the OpenRouter API and returns the response.
func (c *OpenRouterClient) SendPrompt(ctx context.Context, prompt string, history []models.Message, maxTokens int) (string, error) {
	effectiveMaxTokens := maxTokens
	if effectiveMaxTokens <= 0 {
		effectiveMaxTokens = c.getMaxTokens()
	}

	messages := c.buildMessages(prompt, history)
	payload := c.buildPayload(messages, effectiveMaxTokens)

	jsonValue, err := json.Marshal(payload)
	if err != nil {
		c.logger.Error(i18n.T("llm.error.marshal_payload_for", "OpenRouter"), zap.Error(err))
		return "", fmt.Errorf("%s: %w", i18n.T("llm.error.prepare_request"), err)
	}

	response, err := utils.Retry(ctx, c.logger, c.maxAttempts, c.backoff, func(ctx context.Context) (string, error) {
		resp, err := c.sendRequest(ctx, jsonValue)
		if err != nil {
			return "", err
		}
		return c.processResponse(resp)
	})

	if err != nil {
		c.logger.Error(i18n.T("llm.error.get_response_after_retries", "OpenRouter"), zap.Error(err))
		return "", err
	}

	return response, nil
}

// sendRequest sends the HTTP request to the OpenRouter API with proper headers.
func (c *OpenRouterClient) sendRequest(ctx context.Context, jsonValue []byte) (*http.Response, error) {
	apiURL := c.getAPIURL()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, utils.NewJSONReader(jsonValue))
	if err != nil {
		c.logger.Error(i18n.T("llm.error.create_request"), zap.Error(err))
		return nil, fmt.Errorf("%s: %w", i18n.T("llm.error.create_request"), err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+auth.StripAuthPrefix(c.apiKey))

	// OpenRouter-specific headers for attribution and analytics
	if referer := os.Getenv("OPENROUTER_HTTP_REFERER"); referer != "" {
		req.Header.Set("HTTP-Referer", referer)
	}
	if title := os.Getenv("OPENROUTER_APP_TITLE"); title != "" {
		req.Header.Set("X-Title", title)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}

	return resp, nil
}

// processResponse parses the OpenRouter API response, handling both
// standard content responses and tool call responses.
func (c *OpenRouterClient) processResponse(resp *http.Response) (string, error) {
	defer func() { _ = resp.Body.Close() }()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		c.logger.Error(i18n.T("llm.error.read_response_for", "OpenRouter"), zap.Error(err))
		return "", fmt.Errorf("%s: %w", i18n.T("llm.error.read_response_for", "OpenRouter"), err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", &utils.APIError{
			StatusCode: resp.StatusCode,
			Message:    utils.SanitizeSensitiveText(string(bodyBytes)),
		}
	}

	var result openRouterResponse
	if err := json.Unmarshal(bodyBytes, &result); err != nil {
		c.logger.Error(i18n.T("llm.error.decode_response_json_for", "OpenRouter"),
			zap.Error(err), zap.Int("body_size", len(bodyBytes)))
		return "", fmt.Errorf("%s: %w", i18n.T("llm.error.decode_response_for", "OpenRouter"), err)
	}

	// Check for explicit error in response body (OpenRouter may return errors with 200)
	if result.Error != nil && result.Error.Message != "" {
		c.logger.Error(i18n.T("llm.error.api_error_payload", "OpenRouter"),
			zap.String("error_message", result.Error.Message),
			zap.Int("error_code", result.Error.Code))
		return "", fmt.Errorf("%s", i18n.T("llm.error.api_error", "OpenRouter", result.Error.Message))
	}

	if len(result.Choices) == 0 {
		c.logger.Warn(i18n.T("llm.warn.empty_choices", "OpenRouter"),
			zap.Int("body_size", len(bodyBytes)))
		return "", errors.New(i18n.T("llm.error.no_choices", "OpenRouter"))
	}

	firstChoice := result.Choices[0]

	// Handle tool calls: serialize them as JSON so the caller can process them
	if len(firstChoice.Message.ToolCalls) > 0 {
		toolCallsJSON, err := json.Marshal(firstChoice.Message.ToolCalls)
		if err != nil {
			return "", fmt.Errorf("failed to marshal tool calls: %w", err)
		}
		// Return tool calls as structured JSON with a marker prefix
		return fmt.Sprintf("[TOOL_CALLS]%s", string(toolCallsJSON)), nil
	}

	content := firstChoice.Message.Content
	if content == "" {
		c.logger.Warn(i18n.T("llm.warn.empty_content", "OpenRouter"),
			zap.String("finish_reason", firstChoice.FinishReason),
			zap.Int("body_size", len(bodyBytes)))

		if firstChoice.FinishReason == "length" {
			return "", errors.New(i18n.T("llm.error.empty_response_max_tokens", "OpenRouter"))
		}
		return "", errors.New(i18n.T("llm.error.empty_response_unspecified", "OpenRouter"))
	}

	// Log usage metadata for cost tracking
	if result.Usage != nil {
		c.logger.Debug("OpenRouter usage",
			zap.Int("prompt_tokens", result.Usage.PromptTokens),
			zap.Int("completion_tokens", result.Usage.CompletionTokens),
			zap.Int("total_tokens", result.Usage.TotalTokens))
	}

	return content, nil
}

// ListModels fetches available models from the OpenRouter /api/v1/models endpoint.
// This endpoint does not require authentication.
func (c *OpenRouterClient) ListModels(ctx context.Context) ([]client.ModelInfo, error) {
	baseURL := c.getAPIURL()
	modelsURL := strings.TrimSuffix(baseURL, "/chat/completions") + "/models"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, modelsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", i18n.T("llm.error.create_request"), err)
	}
	// Auth is optional for /models but included for consistency
	req.Header.Set("Authorization", "Bearer "+auth.StripAuthPrefix(c.apiKey))

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", i18n.T("llm.error.request_failed", "OpenRouter"), err)
	}
	defer func() { _ = resp.Body.Close() }()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", i18n.T("llm.error.read_response_for", "OpenRouter"), err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %d: %s",
			i18n.T("llm.error.request_failed", "OpenRouter"),
			resp.StatusCode,
			utils.SanitizeSensitiveText(string(bodyBytes)))
	}

	var result openRouterModelsResponse
	if err := json.Unmarshal(bodyBytes, &result); err != nil {
		return nil, fmt.Errorf("%s: %w", i18n.T("llm.error.decode_response_for", "OpenRouter"), err)
	}

	var modelList []client.ModelInfo
	for _, m := range result.Data {
		modelList = append(modelList, client.ModelInfo{
			ID:          m.ID,
			DisplayName: m.Name,
			Source:      client.ModelSourceAPI,
		})

		// Register dynamically discovered models in the catalog
		if _, ok := catalog.Resolve(catalog.ProviderOpenRouter, m.ID); !ok {
			meta := catalog.ModelMeta{
				ID:           m.ID,
				Aliases:      []string{m.ID},
				DisplayName:  m.Name,
				Provider:     catalog.ProviderOpenRouter,
				PreferredAPI: catalog.APIChatCompletions,
			}
			if m.ContextLength > 0 {
				meta.ContextWindow = m.ContextLength
			}
			if m.TopProvider.MaxCompletionTokens > 0 {
				meta.MaxOutputTokens = m.TopProvider.MaxCompletionTokens
			}
			catalog.Register(meta)
		}
	}

	c.logger.Info(i18n.T("llm.info.fetched_models", "OpenRouter"), zap.Int("count", len(modelList)))
	return modelList, nil
}

// --- Response types ---

// openRouterResponse represents the chat completion response from OpenRouter.
type openRouterResponse struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Index   int `json:"index"`
		Message struct {
			Role      string         `json:"role"`
			Content   string         `json:"content"`
			ToolCalls []toolCallInfo `json:"tool_calls,omitempty"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage,omitempty"`
	Error *struct {
		Message string `json:"message"`
		Code    int    `json:"code"`
	} `json:"error,omitempty"`
}

// toolCallInfo represents a tool call in the OpenRouter response.
type toolCallInfo struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// openRouterModelsResponse represents the /models endpoint response.
type openRouterModelsResponse struct {
	Data []openRouterModelEntry `json:"data"`
}

// openRouterModelEntry represents a single model from the /models endpoint.
type openRouterModelEntry struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	ContextLength int    `json:"context_length"`
	Pricing       struct {
		Prompt     string `json:"prompt"`
		Completion string `json:"completion"`
	} `json:"pricing"`
	TopProvider struct {
		MaxCompletionTokens int `json:"max_completion_tokens"`
	} `json:"top_provider"`
}
