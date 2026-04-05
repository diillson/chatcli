/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package minimax

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

	"github.com/diillson/chatcli/config"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/llm/catalog"
	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/utils"
	"go.uber.org/zap"
)

// MiniMaxClient implementa o cliente para interagir com a API da MiniMax.
// A API é compatível com o formato OpenAI (chat/completions).
// When anthropicCompat is true, uses the Anthropic Messages API compatible endpoint.
type MiniMaxClient struct {
	apiKey          string
	model           string
	logger          *zap.Logger
	client          *http.Client
	maxAttempts     int
	backoff         time.Duration
	apiURL          string
	anthropicCompat bool
}

// NewMiniMaxClient cria uma nova instância de MiniMaxClient.
func NewMiniMaxClient(apiKey, model string, logger *zap.Logger, maxAttempts int, backoff time.Duration) *MiniMaxClient {
	httpClient := utils.NewHTTPClient(logger, 900*time.Second)

	apiURL := config.MiniMaxAPIURL
	anthropicCompat := false
	if strings.EqualFold(os.Getenv("MINIMAX_API_COMPAT"), "anthropic") {
		anthropicCompat = true
		apiURL = config.MiniMaxAnthropicAPIURL
		logger.Info(i18n.T("llm.minimax.using_anthropic_compat"))
	}

	return &MiniMaxClient{
		apiKey:          apiKey,
		model:           model, // MiniMax model IDs são case-sensitive (ex: MiniMax-M2.7)
		logger:          logger,
		client:          httpClient,
		maxAttempts:     maxAttempts,
		backoff:         backoff,
		apiURL:          apiURL,
		anthropicCompat: anthropicCompat,
	}
}

// GetModelName retorna o nome amigável do modelo MiniMax via catálogo.
func (c *MiniMaxClient) GetModelName() string {
	return catalog.GetDisplayName(catalog.ProviderMiniMax, c.model)
}

func (c *MiniMaxClient) getMaxTokens() int {
	if tokenStr := os.Getenv("MINIMAX_MAX_TOKENS"); tokenStr != "" {
		if parsedTokens, err := strconv.Atoi(tokenStr); err == nil && parsedTokens > 0 {
			c.logger.Debug(i18n.T("llm.info.using_custom_max_tokens", "MINIMAX_MAX_TOKENS"), zap.Int("max_tokens", parsedTokens))
			return parsedTokens
		}
	}
	return catalog.GetMaxTokens(catalog.ProviderMiniMax, c.model, 0)
}

// SendPrompt envia um prompt para o modelo MiniMax e retorna a resposta.
func (c *MiniMaxClient) SendPrompt(ctx context.Context, prompt string, history []models.Message, maxTokens int) (string, error) {
	effectiveMaxTokens := maxTokens
	if effectiveMaxTokens <= 0 {
		effectiveMaxTokens = c.getMaxTokens()
	}

	messages := []map[string]string{}
	for _, msg := range history {
		role := strings.ToLower(strings.TrimSpace(msg.Role))
		if role != "system" && role != "user" && role != "assistant" {
			role = "user"
		}
		messages = append(messages, map[string]string{"role": role, "content": msg.Content})
	}

	if len(history) == 0 || history[len(history)-1].Role != "user" || history[len(history)-1].Content != prompt {
		if strings.TrimSpace(prompt) != "" {
			messages = append(messages, map[string]string{
				"role":    "user",
				"content": prompt,
			})
		}
	}

	var payload map[string]interface{}
	if c.anthropicCompat {
		payload = c.buildAnthropicPayload(messages, effectiveMaxTokens)
	} else {
		payload = map[string]interface{}{
			"model":      c.model,
			"messages":   messages,
			"max_tokens": effectiveMaxTokens,
		}
	}

	jsonValue, err := json.Marshal(payload)
	if err != nil {
		c.logger.Error(i18n.T("llm.error.marshal_payload_for", "MiniMax"), zap.Error(err))
		return "", fmt.Errorf("%s: %w", i18n.T("llm.error.prepare_request"), err)
	}

	processFunc := c.processResponse
	if c.anthropicCompat {
		processFunc = c.processAnthropicResponse
	}

	response, err := utils.Retry(ctx, c.logger, c.maxAttempts, c.backoff, func(ctx context.Context) (string, error) {
		resp, err := c.sendRequest(ctx, jsonValue)
		if err != nil {
			return "", err
		}
		return processFunc(resp)
	})

	if err != nil {
		c.logger.Error(i18n.T("llm.error.get_response_after_retries", "MiniMax"), zap.Error(err))
		return "", err
	}

	return response, nil
}

// sendRequest envia a requisição para a API da MiniMax.
func (c *MiniMaxClient) sendRequest(ctx context.Context, jsonValue []byte) (*http.Response, error) {
	apiURL := utils.GetEnvOrDefault("MINIMAX_API_URL", c.apiURL)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, utils.NewJSONReader(jsonValue))
	if err != nil {
		c.logger.Error(i18n.T("llm.error.create_request"), zap.Error(err))
		return nil, fmt.Errorf("%s: %w", i18n.T("llm.error.create_request"), err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	if c.anthropicCompat {
		req.Header.Set("anthropic-version", "2023-06-01")
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}

	return resp, nil
}

// processResponse processa a resposta da API da MiniMax.
func (c *MiniMaxClient) processResponse(resp *http.Response) (string, error) {
	defer func() { _ = resp.Body.Close() }()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		c.logger.Error(i18n.T("llm.error.read_response_for", "MiniMax"), zap.Error(err))
		return "", fmt.Errorf("%s: %w", i18n.T("llm.error.read_response_for", "MiniMax"), err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", &utils.APIError{StatusCode: resp.StatusCode, Message: utils.SanitizeSensitiveText(string(bodyBytes))}
	}

	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		BaseResp struct {
			StatusCode int    `json:"status_code"`
			StatusMsg  string `json:"status_msg"`
		} `json:"base_resp"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}

	if err := json.Unmarshal(bodyBytes, &result); err != nil {
		c.logger.Error(i18n.T("llm.error.decode_response_json_for", "MiniMax"), zap.Error(err), zap.Int("body_size", len(bodyBytes)))
		return "", fmt.Errorf("%s: %w", i18n.T("llm.error.decode_response_for", "MiniMax"), err)
	}

	// MiniMax pode retornar erro no campo error (formato OpenAI)
	if result.Error.Message != "" {
		c.logger.Error(i18n.T("llm.error.api_error_payload", "MiniMax"), zap.String("error_message", result.Error.Message))
		return "", fmt.Errorf("%s", i18n.T("llm.error.api_error", "MiniMax", result.Error.Message))
	}

	// MiniMax também usa base_resp para erros em alguns endpoints
	if result.BaseResp.StatusCode != 0 && result.BaseResp.StatusMsg != "" {
		c.logger.Error(i18n.T("llm.error.api_error_base_resp", "MiniMax"),
			zap.Int("status_code", result.BaseResp.StatusCode),
			zap.String("status_msg", result.BaseResp.StatusMsg))
		return "", fmt.Errorf("%s", i18n.T("llm.error.api_error_code", "MiniMax", result.BaseResp.StatusCode, result.BaseResp.StatusMsg))
	}

	if len(result.Choices) == 0 {
		c.logger.Warn(i18n.T("llm.warn.empty_choices", "MiniMax"), zap.Int("body_size", len(bodyBytes)))
		return "", errors.New(i18n.T("llm.error.no_choices", "MiniMax"))
	}

	firstChoice := result.Choices[0]
	if firstChoice.Message.Content == "" {
		c.logger.Warn(i18n.T("llm.warn.empty_content", "MiniMax"),
			zap.String("finish_reason", firstChoice.FinishReason),
			zap.Int("body_size", len(bodyBytes)))

		if firstChoice.FinishReason == "length" {
			return "", errors.New(i18n.T("llm.error.empty_response_max_tokens", "MiniMax"))
		}
		return "", errors.New(i18n.T("llm.error.empty_response_unspecified", "MiniMax"))
	}

	return firstChoice.Message.Content, nil
}

// buildAnthropicPayload converts the standard messages format to the Anthropic Messages API format.
func (c *MiniMaxClient) buildAnthropicPayload(messages []map[string]string, maxTokens int) map[string]interface{} {
	var systemContent string
	var anthropicMessages []map[string]string

	for _, msg := range messages {
		if msg["role"] == "system" {
			systemContent = msg["content"]
		} else {
			anthropicMessages = append(anthropicMessages, map[string]string{
				"role":    msg["role"],
				"content": msg["content"],
			})
		}
	}

	payload := map[string]interface{}{
		"model":      c.model,
		"max_tokens": maxTokens,
		"messages":   anthropicMessages,
	}
	if systemContent != "" {
		payload["system"] = systemContent
	}

	return payload
}

// processAnthropicResponse parses a response in the Anthropic Messages API format.
func (c *MiniMaxClient) processAnthropicResponse(resp *http.Response) (string, error) {
	defer func() { _ = resp.Body.Close() }()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		c.logger.Error(i18n.T("llm.error.read_response_for", "MiniMax"), zap.Error(err))
		return "", fmt.Errorf("%s: %w", i18n.T("llm.error.read_response_for", "MiniMax"), err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", &utils.APIError{StatusCode: resp.StatusCode, Message: utils.SanitizeSensitiveText(string(bodyBytes))}
	}

	var result struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
		Error      struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}

	if err := json.Unmarshal(bodyBytes, &result); err != nil {
		c.logger.Error(i18n.T("llm.error.decode_response_json_for", "MiniMax"), zap.Error(err), zap.Int("body_size", len(bodyBytes)))
		return "", fmt.Errorf("%s: %w", i18n.T("llm.error.decode_response_for", "MiniMax"), err)
	}

	if result.Error.Message != "" {
		c.logger.Error(i18n.T("llm.error.api_error_payload", "MiniMax"), zap.String("error_message", result.Error.Message))
		return "", fmt.Errorf("%s", i18n.T("llm.error.api_error", "MiniMax", result.Error.Message))
	}

	// Extract text from content blocks
	var texts []string
	for _, block := range result.Content {
		if block.Type == "text" && block.Text != "" {
			texts = append(texts, block.Text)
		}
	}

	if len(texts) == 0 {
		c.logger.Warn(i18n.T("llm.warn.empty_content", "MiniMax"), zap.String("stop_reason", result.StopReason))
		if result.StopReason == "max_tokens" {
			return "", errors.New(i18n.T("llm.error.empty_response_max_tokens", "MiniMax"))
		}
		return "", errors.New(i18n.T("llm.error.empty_response_unspecified", "MiniMax"))
	}

	return strings.Join(texts, ""), nil
}

// ListModels fetches available models from the MiniMax API.
func (c *MiniMaxClient) ListModels(ctx context.Context) ([]client.ModelInfo, error) {
	apiURL := utils.GetEnvOrDefault("MINIMAX_API_URL", c.apiURL)
	// When using Anthropic-compat endpoint, fall back to the native API URL for /models
	if c.anthropicCompat && apiURL == config.MiniMaxAnthropicAPIURL {
		apiURL = config.MiniMaxAPIURL
	}
	// Derive /models from base URL (works with both /chat/completions and /text/chatcompletion_v2)
	base := apiURL
	for _, suffix := range []string{"/chat/completions", "/text/chatcompletion_v2", "/text/chatcompletion"} {
		base = strings.TrimSuffix(base, suffix)
	}
	modelsURL := base + "/models"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, modelsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", i18n.T("llm.error.create_request"), err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", i18n.T("llm.error.request_failed", "MiniMax"), err)
	}
	defer func() { _ = resp.Body.Close() }()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", i18n.T("llm.error.read_response_for", "MiniMax"), err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %d: %s", i18n.T("llm.error.request_failed", "MiniMax"), resp.StatusCode, utils.SanitizeSensitiveText(string(bodyBytes)))
	}

	var result struct {
		Data []struct {
			ID      string `json:"id"`
			OwnedBy string `json:"owned_by"`
		} `json:"data"`
	}
	if err := json.Unmarshal(bodyBytes, &result); err != nil {
		return nil, fmt.Errorf("%s: %w", i18n.T("llm.error.decode_response_for", "MiniMax"), err)
	}

	var modelsList []client.ModelInfo
	for _, m := range result.Data {
		id := strings.ToLower(m.ID)
		if !strings.HasPrefix(id, "minimax") && !strings.HasPrefix(id, "abab") {
			continue
		}
		modelsList = append(modelsList, client.ModelInfo{
			ID:          m.ID,
			DisplayName: m.ID,
			Source:      client.ModelSourceAPI,
		})

		if _, ok := catalog.Resolve(catalog.ProviderMiniMax, m.ID); !ok {
			catalog.Register(catalog.ModelMeta{
				ID:           m.ID,
				Aliases:      []string{m.ID},
				DisplayName:  m.ID,
				Provider:     catalog.ProviderMiniMax,
				PreferredAPI: catalog.APIChatCompletions,
			})
		}
	}

	c.logger.Info(i18n.T("llm.info.fetched_models", "MiniMax"), zap.Int("count", len(modelsList)))
	return modelsList, nil
}
