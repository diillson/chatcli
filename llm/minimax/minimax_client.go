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
	"github.com/diillson/chatcli/llm/catalog"
	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/utils"
	"go.uber.org/zap"
)

// MiniMaxClient implementa o cliente para interagir com a API da MiniMax.
// A API é compatível com o formato OpenAI (chat/completions).
type MiniMaxClient struct {
	apiKey      string
	model       string
	logger      *zap.Logger
	client      *http.Client
	maxAttempts int
	backoff     time.Duration
	apiURL      string
}

// NewMiniMaxClient cria uma nova instância de MiniMaxClient.
func NewMiniMaxClient(apiKey, model string, logger *zap.Logger, maxAttempts int, backoff time.Duration) *MiniMaxClient {
	httpClient := utils.NewHTTPClient(logger, 900*time.Second)
	return &MiniMaxClient{
		apiKey:      apiKey,
		model:       model, // MiniMax model IDs são case-sensitive (ex: MiniMax-M2.7)
		logger:      logger,
		client:      httpClient,
		maxAttempts: maxAttempts,
		backoff:     backoff,
		apiURL:      config.MiniMaxAPIURL,
	}
}

// GetModelName retorna o nome amigável do modelo MiniMax via catálogo.
func (c *MiniMaxClient) GetModelName() string {
	return catalog.GetDisplayName(catalog.ProviderMiniMax, c.model)
}

func (c *MiniMaxClient) getMaxTokens() int {
	if tokenStr := os.Getenv("MINIMAX_MAX_TOKENS"); tokenStr != "" {
		if parsedTokens, err := strconv.Atoi(tokenStr); err == nil && parsedTokens > 0 {
			c.logger.Debug("Usando MINIMAX_MAX_TOKENS personalizado", zap.Int("max_tokens", parsedTokens))
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

	payload := map[string]interface{}{
		"model":      c.model,
		"messages":   messages,
		"max_tokens": effectiveMaxTokens,
	}

	jsonValue, err := json.Marshal(payload)
	if err != nil {
		c.logger.Error("Erro ao marshalizar o payload para MiniMax", zap.Error(err))
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
		c.logger.Error("Erro ao obter resposta da MiniMax após retries", zap.Error(err))
		return "", err
	}

	return response, nil
}

// sendRequest envia a requisição para a API da MiniMax.
func (c *MiniMaxClient) sendRequest(ctx context.Context, jsonValue []byte) (*http.Response, error) {
	apiURL := utils.GetEnvOrDefault("MINIMAX_API_URL", c.apiURL)

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

// processResponse processa a resposta da API da MiniMax.
func (c *MiniMaxClient) processResponse(resp *http.Response) (string, error) {
	defer func() { _ = resp.Body.Close() }()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		c.logger.Error("Erro ao ler a resposta da MiniMax", zap.Error(err))
		return "", fmt.Errorf("erro ao ler a resposta da MiniMax: %w", err)
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
		c.logger.Error("Erro ao decodificar a resposta JSON da MiniMax", zap.Error(err), zap.Int("body_size", len(bodyBytes)))
		return "", fmt.Errorf("erro ao decodificar a resposta da MiniMax: %w", err)
	}

	// MiniMax pode retornar erro no campo error (formato OpenAI)
	if result.Error.Message != "" {
		c.logger.Error("API da MiniMax retornou um erro no payload", zap.String("error_message", result.Error.Message))
		return "", fmt.Errorf("erro da API MiniMax: %s", result.Error.Message)
	}

	// MiniMax também usa base_resp para erros em alguns endpoints
	if result.BaseResp.StatusCode != 0 && result.BaseResp.StatusMsg != "" {
		c.logger.Error("API da MiniMax retornou erro via base_resp",
			zap.Int("status_code", result.BaseResp.StatusCode),
			zap.String("status_msg", result.BaseResp.StatusMsg))
		return "", fmt.Errorf("erro da API MiniMax (code %d): %s", result.BaseResp.StatusCode, result.BaseResp.StatusMsg)
	}

	if len(result.Choices) == 0 {
		c.logger.Warn("Nenhuma 'choice' na resposta da MiniMax.", zap.Int("body_size", len(bodyBytes)))
		return "", errors.New("a API da MiniMax não retornou nenhuma resposta (array 'choices' vazio)")
	}

	firstChoice := result.Choices[0]
	if firstChoice.Message.Content == "" {
		c.logger.Warn("Resposta da MiniMax com conteúdo vazio.",
			zap.String("finish_reason", firstChoice.FinishReason),
			zap.Int("body_size", len(bodyBytes)))

		if firstChoice.FinishReason == "length" {
			return "", errors.New("a API da MiniMax retornou uma resposta vazia, provavelmente porque o valor de 'max_tokens' é muito baixo")
		}
		return "", errors.New("a API da MiniMax retornou uma resposta vazia por um motivo não especificado")
	}

	return firstChoice.Message.Content, nil
}

// ListModels fetches available models from the MiniMax API.
func (c *MiniMaxClient) ListModels(ctx context.Context) ([]client.ModelInfo, error) {
	apiURL := utils.GetEnvOrDefault("MINIMAX_API_URL", c.apiURL)
	// MiniMax OpenAI-compatible endpoint: derive /models from base
	modelsURL := strings.TrimSuffix(apiURL, "/chat/completions") + "/models"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, modelsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch MiniMax models: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read models response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("MiniMax /models returned %d: %s", resp.StatusCode, utils.SanitizeSensitiveText(string(bodyBytes)))
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

	c.logger.Info("Fetched MiniMax models", zap.Int("count", len(modelsList)))
	return modelsList, nil
}
