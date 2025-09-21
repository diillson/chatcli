/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
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
	"github.com/diillson/chatcli/llm/catalog"
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
		model:       model,
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
			c.logger.Debug("Usando XAI_MAX_TOKENS personalizado", zap.Int("max_tokens", parsedTokens))
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
		c.logger.Error("Erro ao marshalizar o payload para xAI", zap.Error(err))
		return "", fmt.Errorf("erro ao preparar a requisição: %w", err)
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
		c.logger.Error("Erro ao obter resposta da xAI após retries", zap.Error(err))
		return "", err
	}

	return response, nil
}

func (c *XAIClient) sendRequest(ctx context.Context, jsonValue []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiURL, utils.NewJSONReader(jsonValue))
	if err != nil {
		return nil, fmt.Errorf("erro ao criar a requisição: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	return c.client.Do(req)
}

func (c *XAIClient) processResponse(resp *http.Response) (string, error) {
	defer func() { _ = resp.Body.Close() }()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		c.logger.Error("Erro ao ler a resposta da xAI", zap.Error(err))
		return "", fmt.Errorf("erro ao ler a resposta da xAI: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", &utils.APIError{StatusCode: resp.StatusCode, Message: string(bodyBytes)}
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
		c.logger.Error("Erro ao decodificar a resposta JSON da xAI", zap.Error(err), zap.ByteString("body", bodyBytes))
		return "", fmt.Errorf("erro ao decodificar a resposta da xAI: %w", err)
	}

	// Verificar se a API retornou um erro explícito no corpo (mesmo com status 200)
	if result.Error.Message != "" {
		c.logger.Error("API da xAI retornou um erro no payload", zap.String("error_message", result.Error.Message))
		return "", fmt.Errorf("erro da API xAI: %s", result.Error.Message)
	}

	// Verificar se não há 'choices'
	if len(result.Choices) == 0 {
		c.logger.Warn("Nenhuma 'choice' na resposta da xAI.", zap.ByteString("body", bodyBytes))
		return "", errors.New("a API da xAI não retornou nenhuma resposta (array 'choices' vazio)")
	}

	firstChoice := result.Choices[0]
	if firstChoice.Message.Content == "" {
		c.logger.Warn("Resposta da xAI com conteúdo vazio.",
			zap.String("finish_reason", firstChoice.FinishReason),
			zap.ByteString("body", bodyBytes))

		// Retorna um erro amigável para o usuário
		if firstChoice.FinishReason == "length" {
			return "", errors.New("a API da xAI retornou uma resposta vazia, provavelmente porque o valor de 'max_tokens' é muito baixo")
		}
		return "", errors.New("a API da xAI retornou uma resposta vazia por um motivo não especificado")
	}

	return firstChoice.Message.Content, nil
}
