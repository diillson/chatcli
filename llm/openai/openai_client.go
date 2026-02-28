/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
 */
package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/diillson/chatcli/auth"
	"github.com/diillson/chatcli/config"
	"github.com/diillson/chatcli/llm/catalog"

	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/utils"
	"go.uber.org/zap"
)

// OpenAIClient implementa o cliente para interagir com a API da OpenAI
type OpenAIClient struct {
	apiKey      string
	model       string
	logger      *zap.Logger
	client      *http.Client
	maxAttempts int
	backoff     time.Duration
}

// NewOpenAIClient cria uma nova instância de OpenAIClient.
func NewOpenAIClient(apiKey, model string, logger *zap.Logger, maxAttempts int, backoff time.Duration) *OpenAIClient {
	httpClient := utils.NewHTTPClient(logger, 900*time.Second)

	return &OpenAIClient{
		apiKey:      apiKey,
		model:       model,
		logger:      logger,
		client:      httpClient,
		maxAttempts: maxAttempts,
		backoff:     backoff,
	}
}

// GetModelName retorna o nome amigável do modelo OpenAI via catálogo.
func (c *OpenAIClient) GetModelName() string {
	return catalog.GetDisplayName(catalog.ProviderOpenAI, c.model)
}

func (c *OpenAIClient) getMaxTokens() int {
	if tokenStr := os.Getenv("OPENAI_MAX_TOKENS"); tokenStr != "" {
		if parsedTokens, err := strconv.Atoi(tokenStr); err == nil && parsedTokens > 0 {
			c.logger.Debug("Usando OPENAI_MAX_TOKENS personalizado", zap.Int("max_tokens", parsedTokens))
			return parsedTokens
		}
	}
	// Fallback para catálogo
	return catalog.GetMaxTokens(catalog.ProviderOpenAI, c.model, 0)
}

// SendPrompt envia um prompt para o modelo de linguagem e retorna a resposta.
func (c *OpenAIClient) SendPrompt(ctx context.Context, prompt string, history []models.Message, maxTokens int) (string, error) {
	effectiveMaxTokens := maxTokens
	if effectiveMaxTokens <= 0 {
		effectiveMaxTokens = c.getMaxTokens() // valor padrão
	}

	// Construir o array de mensagens APENAS a partir do history
	messages := []map[string]string{}

	for _, msg := range history {
		role := strings.ToLower(strings.TrimSpace(msg.Role))
		switch role {
		case "system", "user", "assistant":
			// válido
		default:
			role = "user"
		}
		messages = append(messages, map[string]string{
			"role":    role,
			"content": msg.Content,
		})
	}

	// Fallback: se history não tem o último prompt do user (edge-case),
	// adiciona o "prompt" como user aqui.
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
		c.logger.Error("Erro ao marshalizar o payload", zap.Error(err))
		return "", fmt.Errorf("erro ao preparar a requisição: %w", err)
	}

	response, err := utils.Retry(ctx, c.logger, c.maxAttempts, c.backoff, func(ctx context.Context) (string, error) {
		resp, err := c.sendRequest(ctx, jsonValue)
		if err != nil {
			return "", err
		}
		return c.processResponse(resp)
	})

	return response, err
}

// sendRequest envia a requisição para a API da OpenAI
func (c *OpenAIClient) sendRequest(ctx context.Context, jsonValue []byte) (*http.Response, error) {
	apiURL := utils.GetEnvOrDefault("OPENAI_API_URL", config.OpenAIAPIURL)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, utils.NewJSONReader(jsonValue))
	if err != nil {
		c.logger.Error("Erro ao criar a requisição", zap.Error(err))
		return nil, fmt.Errorf("erro ao criar a requisição: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+auth.StripAuthPrefix(c.apiKey))

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}

	return resp, nil
}

// processResponse processa a resposta da API da OpenAI
func (c *OpenAIClient) processResponse(resp *http.Response) (string, error) {
	defer func() { _ = resp.Body.Close() }()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		c.logger.Error("Erro ao ler a resposta da OpenAI", zap.Error(err))
		return "", fmt.Errorf("erro ao ler a resposta da OpenAI: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", &utils.APIError{StatusCode: resp.StatusCode, Message: utils.SanitizeSensitiveText(string(bodyBytes))}
	}

	var result map[string]interface{}
	// CORREÇÃO AQUI: Use Unmarshal com bodyBytes
	if err := json.Unmarshal(bodyBytes, &result); err != nil {
		c.logger.Error("Erro ao decodificar a resposta da OpenAI", zap.Error(err))
		return "", fmt.Errorf("erro ao decodificar a resposta da OpenAI: %w", err)
	}

	choices, ok := result["choices"].([]interface{})
	if !ok || len(choices) == 0 {
		c.logger.Error("Nenhuma resposta recebida da OpenAI", zap.Any("resultado", result))
		return "", fmt.Errorf("nenhuma resposta recebida da OpenAI")
	}

	firstChoice, ok := choices[0].(map[string]interface{})
	if !ok {
		c.logger.Error("Formato inesperado no primeiro choice", zap.Any("choice", choices[0]))
		return "", fmt.Errorf("formato inesperado na resposta da OpenAI")
	}

	message, ok := firstChoice["message"].(map[string]interface{})
	if !ok {
		c.logger.Error("Campo 'message' ausente na resposta", zap.Any("choice", firstChoice))
		return "", fmt.Errorf("campo 'message' ausente na resposta da OpenAI")
	}

	content, ok := message["content"].(string)
	if !ok {
		c.logger.Error("Conteúdo da mensagem não é uma string", zap.Any("content", message["content"]))
		return "", fmt.Errorf("conteúdo da mensagem não é válido")
	}

	return content, nil
}
