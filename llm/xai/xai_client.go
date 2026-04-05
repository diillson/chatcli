/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package xai

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

type XAIClient struct {
	apiKey      string
	model       string
	logger      *zap.Logger
	client      *http.Client
	maxAttempts int
	backoff     time.Duration
	apiURL      string
}

// NewXAIClient cria uma nova instância de XAIClient.
func NewXAIClient(apiKey, model string, logger *zap.Logger, maxAttempts int, backoff time.Duration) *XAIClient {
	httpClient := utils.NewHTTPClient(logger, 900*time.Second)
	return &XAIClient{
		apiKey:      apiKey,
		model:       strings.ToLower(model),
		logger:      logger,
		client:      httpClient,
		maxAttempts: maxAttempts,
		backoff:     backoff,
		apiURL:      config.XAIAPIURL,
	}
}

// GetModelName retorna o nome do modelo de linguagem utilizado.
func (c *XAIClient) GetModelName() string {
	return catalog.GetDisplayName(catalog.ProviderXAI, c.model)
}

func (c *XAIClient) getMaxTokens() int {
	if tokenStr := os.Getenv("XAI_MAX_TOKENS"); tokenStr != "" {
		if parsedTokens, err := strconv.Atoi(tokenStr); err == nil && parsedTokens > 0 {
			c.logger.Debug(i18n.T("llm.info.using_custom_max_tokens", "XAI_MAX_TOKENS"), zap.Int("max_tokens", parsedTokens))
			return parsedTokens
		}
	}
	// Fallback para catálogo
	return catalog.GetMaxTokens(catalog.ProviderXAI, c.model, 0)
}

// SendPrompt envia um prompt para o modelo e retorna a resposta.
func (c *XAIClient) SendPrompt(ctx context.Context, prompt string, history []models.Message, maxTokens int) (string, error) {
	effectiveMaxTokens := maxTokens
	if effectiveMaxTokens <= 0 {
		effectiveMaxTokens = c.getMaxTokens() // Fallback para a lógica antiga se nada for passado
	}

	messages := []map[string]string{}
	for _, msg := range history {
		role := strings.ToLower(strings.TrimSpace(msg.Role))
		if role != "system" && role != "user" && role != "assistant" {
			role = "user"
		}
		messages = append(messages, map[string]string{"role": role, "content": msg.Content})
	}

	payload := map[string]interface{}{
		"model":      c.model,
		"messages":   messages,
		"max_tokens": effectiveMaxTokens,
	}

	jsonValue, err := json.Marshal(payload)
	if err != nil {
		c.logger.Error(i18n.T("llm.error.marshal_payload_for", "xAI"), zap.Error(err))
		return "", fmt.Errorf("%s: %w", i18n.T("llm.error.prepare_request"), err)
	}

	// Agora use Retry para encapsular a lógica de requisição e parsing
	response, err := utils.Retry(ctx, c.logger, c.maxAttempts, c.backoff, func(ctx context.Context) (string, error) {
		resp, err := c.sendRequest(ctx, jsonValue)
		if err != nil {
			return "", err
		}
		return c.processResponse(resp)
	})

	if err != nil {
		c.logger.Error(i18n.T("llm.error.get_response_after_retries", "xAI"), zap.Error(err))
		return "", err
	}

	return response, nil
}

func (c *XAIClient) sendRequest(ctx context.Context, jsonValue []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiURL, utils.NewJSONReader(jsonValue))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", i18n.T("llm.error.create_request"), err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	return c.client.Do(req)
}

func (c *XAIClient) processResponse(resp *http.Response) (string, error) {
	defer func() { _ = resp.Body.Close() }()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		c.logger.Error(i18n.T("llm.error.read_response_for", "xAI"), zap.Error(err))
		return "", fmt.Errorf("%s: %w", i18n.T("llm.error.read_response_for", "xAI"), err)
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
		Error struct { // Também verificamos um possível campo de erro explícito
			Message string `json:"message"`
		} `json:"error"`
	}

	if err := json.Unmarshal(bodyBytes, &result); err != nil {
		c.logger.Error(i18n.T("llm.error.decode_response_json_for", "xAI"), zap.Error(err), zap.Int("body_size", len(bodyBytes)))
		return "", fmt.Errorf("%s: %w", i18n.T("llm.error.decode_response_for", "xAI"), err)
	}

	// Verificar se a API retornou um erro explícito no corpo (mesmo com status 200)
	if result.Error.Message != "" {
		c.logger.Error(i18n.T("llm.error.api_error_payload", "xAI"), zap.String("error_message", result.Error.Message))
		return "", fmt.Errorf("%s", i18n.T("llm.error.api_error", "xAI", result.Error.Message))
	}

	// Verificar se não há 'choices'
	if len(result.Choices) == 0 {
		c.logger.Warn(i18n.T("llm.warn.empty_choices", "xAI"), zap.Int("body_size", len(bodyBytes)))
		return "", errors.New(i18n.T("llm.error.no_choices", "xAI"))
	}

	firstChoice := result.Choices[0]
	if firstChoice.Message.Content == "" {
		c.logger.Warn(i18n.T("llm.warn.empty_content", "xAI"),
			zap.String("finish_reason", firstChoice.FinishReason),
			zap.Int("body_size", len(bodyBytes)))

		if firstChoice.FinishReason == "length" {
			return "", errors.New(i18n.T("llm.error.empty_response_max_tokens", "xAI"))
		}
		return "", errors.New(i18n.T("llm.error.empty_response_unspecified", "xAI"))
	}

	return firstChoice.Message.Content, nil
}

// ListModels fetches available models from the xAI /v1/models endpoint.
func (c *XAIClient) ListModels(ctx context.Context) ([]client.ModelInfo, error) {
	// Derive models URL from the chat completions URL
	modelsURL := strings.TrimSuffix(c.apiURL, "/chat/completions") + "/models"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, modelsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", i18n.T("llm.error.create_request"), err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", i18n.T("llm.error.request_failed", "xAI"), err)
	}
	defer func() { _ = resp.Body.Close() }()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", i18n.T("llm.error.read_response_for", "xAI"), err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %d: %s", i18n.T("llm.error.request_failed", "xAI"), resp.StatusCode, utils.SanitizeSensitiveText(string(bodyBytes)))
	}

	var result struct {
		Data []struct {
			ID      string `json:"id"`
			OwnedBy string `json:"owned_by"`
		} `json:"data"`
	}
	if err := json.Unmarshal(bodyBytes, &result); err != nil {
		return nil, fmt.Errorf("%s: %w", i18n.T("llm.error.decode_response_for", "xAI"), err)
	}

	var modelList []client.ModelInfo
	for _, m := range result.Data {
		modelList = append(modelList, client.ModelInfo{
			ID:          m.ID,
			DisplayName: m.ID,
			Source:      client.ModelSourceAPI,
		})

		if _, ok := catalog.Resolve(catalog.ProviderXAI, m.ID); !ok {
			catalog.Register(catalog.ModelMeta{
				ID:           m.ID,
				Aliases:      []string{m.ID},
				DisplayName:  m.ID,
				Provider:     catalog.ProviderXAI,
				PreferredAPI: catalog.APIChatCompletions,
			})
		}
	}

	c.logger.Info(i18n.T("llm.info.fetched_models", "xAI"), zap.Int("count", len(modelList)))
	return modelList, nil
}
