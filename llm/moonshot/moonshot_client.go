/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package moonshot

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

// MoonshotClient implementa o cliente para a API Moonshot (Kimi).
// A API é OpenAI-compatible (chat/completions) com extensões opcionais:
//   - `extra_body.thinking.type` aceita "enabled"/"disabled" para chavear
//     entre o modo "Thinking" (reasoning explícito) e o modo "Instant".
//   - cache automático de contexto (cache_hit reflete no preço, não no payload).
//
// Modelo default: kimi-k2.6 (256K contexto, multimodal, thinking).
type MoonshotClient struct {
	provider     auth.TokenProvider
	model        string
	logger       *zap.Logger
	client       *http.Client
	maxAttempts  int
	backoff      time.Duration
	apiURL       string
	thinkingMode string // "auto" (default per-model), "enabled", "disabled"
	usageState   client.UsageState
}

// LastUsage returns the token usage from the most recent API call.
func (c *MoonshotClient) LastUsage() *models.UsageInfo { return c.usageState.LastUsage() }

// LastStopReason returns the stop reason from the most recent API call.
func (c *MoonshotClient) LastStopReason() string { return c.usageState.LastStopReason() }

// NewMoonshotClient cria uma nova instância de MoonshotClient.
func NewMoonshotClient(provider auth.TokenProvider, model string, logger *zap.Logger, maxAttempts int, backoff time.Duration) *MoonshotClient {
	httpClient := utils.NewHTTPClient(logger, 900*time.Second)
	return &MoonshotClient{
		provider:     provider,
		model:        strings.ToLower(model),
		logger:       logger,
		client:       httpClient,
		maxAttempts:  maxAttempts,
		backoff:      backoff,
		apiURL:       config.MoonshotAPIURL,
		thinkingMode: strings.ToLower(strings.TrimSpace(os.Getenv("MOONSHOT_THINKING"))),
	}
}

// GetModelName retorna o nome amigável do modelo Moonshot via catálogo.
func (c *MoonshotClient) GetModelName() string {
	return catalog.GetDisplayName(catalog.ProviderMoonshot, c.model)
}

func (c *MoonshotClient) getMaxTokens() int {
	if tokenStr := os.Getenv("MOONSHOT_MAX_TOKENS"); tokenStr != "" {
		if parsedTokens, err := strconv.Atoi(tokenStr); err == nil && parsedTokens > 0 {
			c.logger.Debug(i18n.T("llm.info.using_custom_max_tokens", "MOONSHOT_MAX_TOKENS"), zap.Int("max_tokens", parsedTokens))
			return parsedTokens
		}
	}
	return catalog.GetMaxTokens(catalog.ProviderMoonshot, c.model, 0)
}

// supportsThinking reports whether the resolved model exposes thinking mode.
func (c *MoonshotClient) supportsThinking() bool {
	if meta, ok := catalog.Resolve(catalog.ProviderMoonshot, c.model); ok {
		for _, cap := range meta.Capabilities {
			if cap == "thinking" {
				return true
			}
		}
	}
	return false
}

// applyThinking injects extra_body.thinking into the payload when the user
// requested an explicit mode and the model supports it. "auto" leaves the
// payload untouched (Moonshot picks the model's default).
func (c *MoonshotClient) applyThinking(payload map[string]interface{}) {
	if !c.supportsThinking() {
		return
	}
	switch c.thinkingMode {
	case "enabled", "on", "true":
		payload["extra_body"] = map[string]interface{}{
			"thinking": map[string]interface{}{"type": "enabled"},
		}
	case "disabled", "off", "false", "instant":
		payload["extra_body"] = map[string]interface{}{
			"thinking": map[string]interface{}{"type": "disabled"},
		}
	}
}

// SendPrompt envia um prompt para o modelo Moonshot (Kimi) e retorna a resposta.
func (c *MoonshotClient) SendPrompt(ctx context.Context, prompt string, history []models.Message, maxTokens int) (string, error) {
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

	payload := map[string]interface{}{
		"model":      c.model,
		"messages":   messages,
		"max_tokens": effectiveMaxTokens,
	}
	c.applyThinking(payload)

	jsonValue, err := json.Marshal(payload)
	if err != nil {
		c.logger.Error(i18n.T("llm.error.marshal_payload_for", "MOONSHOT"), zap.Error(err))
		return "", fmt.Errorf("%s: %w", i18n.T("llm.error.prepare_request"), err)
	}

	start := time.Now()
	client.LogRequestStart(c.logger, "MOONSHOT", c.model,
		zap.Int("payload_bytes", len(jsonValue)),
		zap.Int("history_len", len(history)),
		zap.Int("max_tokens", effectiveMaxTokens),
	)

	response, err := utils.Retry(ctx, c.logger, c.maxAttempts, c.backoff, func(ctx context.Context) (string, error) {
		//nolint:bodyclose // processResponse takes ownership of resp
		// and defers Body.Close(); closing here would double-close.
		resp, err := auth.DoWithRefresh(ctx, c.provider, func(token string) (*http.Response, error) {
			return c.sendRequest(ctx, jsonValue, token)
		})
		if err != nil {
			return "", err
		}
		return c.processResponse(resp)
	})

	if err != nil {
		client.LogRequestFinish(c.logger, "MOONSHOT", c.model, "error", time.Since(start))
		c.logger.Error(i18n.T("llm.error.get_response_after_retries", "MOONSHOT"), zap.Error(err))
		return "", err
	}

	client.LogRequestFinish(c.logger, "MOONSHOT", c.model, "success", time.Since(start),
		zap.Int("response_chars", len(response)),
	)
	return response, nil
}

// sendRequest envia a requisição para a API Moonshot.
func (c *MoonshotClient) sendRequest(ctx context.Context, jsonValue []byte, token string) (*http.Response, error) {
	apiURL := utils.GetEnvOrDefault("MOONSHOT_API_URL", c.apiURL)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, utils.NewJSONReader(jsonValue))
	if err != nil {
		c.logger.Error(i18n.T("llm.error.create_request"), zap.Error(err))
		return nil, fmt.Errorf("%s: %w", i18n.T("llm.error.create_request"), err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	return c.client.Do(req)
}

// processResponse processa a resposta da API Moonshot.
func (c *MoonshotClient) processResponse(resp *http.Response) (string, error) {
	defer func() { _ = resp.Body.Close() }()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		c.logger.Error(i18n.T("llm.error.read_response_for", "MOONSHOT"), zap.Error(err))
		return "", fmt.Errorf("%s: %w", i18n.T("llm.error.read_response_for", "MOONSHOT"), err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", &utils.APIError{StatusCode: resp.StatusCode, Message: utils.SanitizeSensitiveText(string(bodyBytes))}
	}

	var result struct {
		Choices []struct {
			Message struct {
				Content   string `json:"content"`
				Reasoning string `json:"reasoning,omitempty"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}

	if err := json.Unmarshal(bodyBytes, &result); err != nil {
		c.logger.Error(i18n.T("llm.error.decode_response_json_for", "MOONSHOT"), zap.Error(err), zap.Int("body_size", len(bodyBytes)))
		return "", fmt.Errorf("%s: %w", i18n.T("llm.error.decode_response_for", "MOONSHOT"), err)
	}

	// Extract usage and stop reason for cost tracking.
	var rawResult map[string]interface{}
	if err := json.Unmarshal(bodyBytes, &rawResult); err == nil {
		if usage := client.ParseOpenAIUsage(rawResult); usage != nil {
			c.usageState.StoreUsage(usage)
		}
		c.usageState.StoreStopReason(client.ParseOpenAIFinishReason(rawResult))
	}

	if result.Error.Message != "" {
		c.logger.Error(i18n.T("llm.error.api_error_payload", "MOONSHOT"), zap.String("error_message", result.Error.Message))
		return "", fmt.Errorf("%s", i18n.T("llm.error.api_error", "MOONSHOT", result.Error.Message))
	}

	if len(result.Choices) == 0 {
		c.logger.Warn(i18n.T("llm.warn.empty_choices", "MOONSHOT"), zap.Int("body_size", len(bodyBytes)))
		return "", errors.New(i18n.T("llm.error.no_choices", "MOONSHOT"))
	}

	firstChoice := result.Choices[0]
	if firstChoice.Message.Content == "" {
		c.logger.Warn(i18n.T("llm.warn.empty_content", "MOONSHOT"),
			zap.String("finish_reason", firstChoice.FinishReason),
			zap.Int("body_size", len(bodyBytes)))

		if firstChoice.FinishReason == "length" {
			return "", errors.New(i18n.T("llm.error.empty_response_max_tokens", "MOONSHOT"))
		}
		return "", errors.New(i18n.T("llm.error.empty_response_unspecified", "MOONSHOT"))
	}

	return firstChoice.Message.Content, nil
}

// ListModels fetches available models from the Moonshot /models endpoint.
// Registers any unknown Kimi/Moonshot models into the catalog so they become
// auto-completable without a code change.
func (c *MoonshotClient) ListModels(ctx context.Context) ([]client.ModelInfo, error) {
	apiURL := utils.GetEnvOrDefault("MOONSHOT_API_URL", c.apiURL)
	modelsURL := strings.TrimSuffix(apiURL, "/chat/completions") + "/models"

	resp, err := auth.DoWithRefresh(ctx, c.provider, func(token string) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, modelsURL, nil)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", i18n.T("llm.error.create_request"), err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		return c.client.Do(req)
	})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", i18n.T("llm.error.request_failed", "MOONSHOT"), err)
	}
	defer func() { _ = resp.Body.Close() }()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", i18n.T("llm.error.read_response_for", "MOONSHOT"), err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %d: %s", i18n.T("llm.error.request_failed", "MOONSHOT"), resp.StatusCode, utils.SanitizeSensitiveText(string(bodyBytes)))
	}

	var result struct {
		Data []struct {
			ID      string `json:"id"`
			OwnedBy string `json:"owned_by"`
		} `json:"data"`
	}
	if err := json.Unmarshal(bodyBytes, &result); err != nil {
		return nil, fmt.Errorf("%s: %w", i18n.T("llm.error.decode_response_for", "MOONSHOT"), err)
	}

	modelsList := make([]client.ModelInfo, 0, len(result.Data))
	for _, m := range result.Data {
		id := strings.ToLower(m.ID)
		if !strings.HasPrefix(id, "kimi") && !strings.HasPrefix(id, "moonshot") {
			continue
		}
		modelsList = append(modelsList, client.ModelInfo{
			ID:          m.ID,
			DisplayName: m.ID,
			Source:      client.ModelSourceAPI,
		})

		if _, ok := catalog.Resolve(catalog.ProviderMoonshot, m.ID); !ok {
			catalog.Register(catalog.ModelMeta{
				ID:           m.ID,
				Aliases:      []string{m.ID},
				DisplayName:  m.ID,
				Provider:     catalog.ProviderMoonshot,
				PreferredAPI: catalog.APIChatCompletions,
			})
		}
	}

	c.logger.Info(i18n.T("llm.info.fetched_models", "MOONSHOT"), zap.Int("count", len(modelsList)))
	return modelsList, nil
}
