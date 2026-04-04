/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package zai

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
	"github.com/diillson/chatcli/llm/catalog"
	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/utils"
	"go.uber.org/zap"
)

// ZAIClient implementa o cliente para interagir com a API da ZAI (Zhipu AI / z.ai).
// A API é compatível com o formato OpenAI (chat/completions).
type ZAIClient struct {
	apiKey      string
	model       string
	logger      *zap.Logger
	client      *http.Client
	maxAttempts int
	backoff     time.Duration
	apiURL      string
}

// NewZAIClient cria uma nova instância de ZAIClient.
func NewZAIClient(apiKey, model string, logger *zap.Logger, maxAttempts int, backoff time.Duration) *ZAIClient {
	httpClient := utils.NewHTTPClient(logger, 900*time.Second)
	return &ZAIClient{
		apiKey:      apiKey,
		model:       strings.ToLower(model),
		logger:      logger,
		client:      httpClient,
		maxAttempts: maxAttempts,
		backoff:     backoff,
		apiURL:      config.ZAIAPIURL,
	}
}

// GetModelName retorna o nome amigável do modelo ZAI via catálogo.
func (c *ZAIClient) GetModelName() string {
	return catalog.GetDisplayName(catalog.ProviderZAI, c.model)
}

func (c *ZAIClient) getMaxTokens() int {
	if tokenStr := os.Getenv("ZAI_MAX_TOKENS"); tokenStr != "" {
		if parsedTokens, err := strconv.Atoi(tokenStr); err == nil && parsedTokens > 0 {
			c.logger.Debug("Usando ZAI_MAX_TOKENS personalizado", zap.Int("max_tokens", parsedTokens))
			return parsedTokens
		}
	}
	return catalog.GetMaxTokens(catalog.ProviderZAI, c.model, 0)
}

// SendPrompt envia um prompt para o modelo ZAI e retorna a resposta.
func (c *ZAIClient) SendPrompt(ctx context.Context, prompt string, history []models.Message, maxTokens int) (string, error) {
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

	jsonValue, err := json.Marshal(payload)
	if err != nil {
		c.logger.Error("Erro ao marshalizar o payload para ZAI", zap.Error(err))
		return "", fmt.Errorf("erro ao preparar a requisição: %w", err)
	}

	response, err := utils.Retry(ctx, c.logger, c.maxAttempts, c.backoff, func(ctx context.Context) (string, error) {
		resp, err := c.sendRequest(ctx, jsonValue)
		if err != nil {
			return "", err
		}
		return c.processResponse(resp)
	})

	if err != nil {
		c.logger.Error("Erro ao obter resposta da ZAI após retries", zap.Error(err))
		return "", err
	}

	return response, nil
}

// sendRequest envia a requisição para a API da ZAI.
func (c *ZAIClient) sendRequest(ctx context.Context, jsonValue []byte) (*http.Response, error) {
	apiURL := utils.GetEnvOrDefault("ZAI_API_URL", c.apiURL)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, utils.NewJSONReader(jsonValue))
	if err != nil {
		c.logger.Error("Erro ao criar a requisição", zap.Error(err))
		return nil, fmt.Errorf("erro ao criar a requisição: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}

	return resp, nil
}

// processResponse processa a resposta da API da ZAI.
func (c *ZAIClient) processResponse(resp *http.Response) (string, error) {
	defer func() { _ = resp.Body.Close() }()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		c.logger.Error("Erro ao ler a resposta da ZAI", zap.Error(err))
		return "", fmt.Errorf("erro ao ler a resposta da ZAI: %w", err)
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
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}

	if err := json.Unmarshal(bodyBytes, &result); err != nil {
		c.logger.Error("Erro ao decodificar a resposta JSON da ZAI", zap.Error(err), zap.Int("body_size", len(bodyBytes)))
		return "", fmt.Errorf("erro ao decodificar a resposta da ZAI: %w", err)
	}

	if result.Error.Message != "" {
		c.logger.Error("API da ZAI retornou um erro no payload", zap.String("error_message", result.Error.Message))
		return "", fmt.Errorf("erro da API ZAI: %s", result.Error.Message)
	}

	if len(result.Choices) == 0 {
		c.logger.Warn("Nenhuma 'choice' na resposta da ZAI.", zap.Int("body_size", len(bodyBytes)))
		return "", errors.New("a API da ZAI não retornou nenhuma resposta (array 'choices' vazio)")
	}

	firstChoice := result.Choices[0]
	if firstChoice.Message.Content == "" {
		c.logger.Warn("Resposta da ZAI com conteúdo vazio.",
			zap.String("finish_reason", firstChoice.FinishReason),
			zap.Int("body_size", len(bodyBytes)))

		if firstChoice.FinishReason == "length" {
			return "", errors.New("a API da ZAI retornou uma resposta vazia, provavelmente porque o valor de 'max_tokens' é muito baixo")
		}
		return "", errors.New("a API da ZAI retornou uma resposta vazia por um motivo não especificado")
	}

	return firstChoice.Message.Content, nil
}

// ListModels fetches available models from the ZAI /models endpoint.
func (c *ZAIClient) ListModels(ctx context.Context) ([]client.ModelInfo, error) {
	apiURL := utils.GetEnvOrDefault("ZAI_API_URL", c.apiURL)
	modelsURL := strings.TrimSuffix(apiURL, "/chat/completions") + "/models"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, modelsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch ZAI models: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read models response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ZAI /models returned %d: %s", resp.StatusCode, utils.SanitizeSensitiveText(string(bodyBytes)))
	}

	var result struct {
		Data []struct {
			ID      string `json:"id"`
			OwnedBy string `json:"owned_by"`
		} `json:"data"`
	}
	if err := json.Unmarshal(bodyBytes, &result); err != nil {
		return nil, fmt.Errorf("failed to decode models response: %w", err)
	}

	var modelsList []client.ModelInfo
	for _, m := range result.Data {
		id := strings.ToLower(m.ID)
		if !strings.HasPrefix(id, "glm-") && !strings.HasPrefix(id, "codegeex") &&
			!strings.HasPrefix(id, "cogview") && !strings.HasPrefix(id, "charglm") {
			continue
		}
		modelsList = append(modelsList, client.ModelInfo{
			ID:          m.ID,
			DisplayName: m.ID,
			Source:      client.ModelSourceAPI,
		})

		if _, ok := catalog.Resolve(catalog.ProviderZAI, m.ID); !ok {
			catalog.Register(catalog.ModelMeta{
				ID:           m.ID,
				Aliases:      []string{m.ID},
				DisplayName:  m.ID,
				Provider:     catalog.ProviderZAI,
				PreferredAPI: catalog.APIChatCompletions,
			})
		}
	}

	c.logger.Info("Fetched ZAI models", zap.Int("count", len(modelsList)))
	return modelsList, nil
}
