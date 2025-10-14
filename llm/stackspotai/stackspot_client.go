/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
 */
package stackspotai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/diillson/chatcli/config"
	"github.com/diillson/chatcli/llm/token"
	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/utils"
	"go.uber.org/zap"
)

// StackSpotClient implementa o cliente para interagir com a API de Agente da StackSpot
type StackSpotClient struct {
	tokenManager token.Manager
	agentID      string
	logger       *zap.Logger
	client       *http.Client
	maxAttempts  int
	backoff      time.Duration
	baseURL      string
}

// NewStackSpotClient cria uma nova instância de StackSpotClient.
func NewStackSpotClient(tokenManager token.Manager, agentID string, logger *zap.Logger, maxAttempts int, backoff time.Duration) *StackSpotClient {
	httpClient := utils.NewHTTPClient(logger, 900*time.Second)
	return &StackSpotClient{
		tokenManager: tokenManager,
		agentID:      agentID,
		logger:       logger,
		client:       httpClient,
		maxAttempts:  maxAttempts,
		backoff:      backoff,
		baseURL:      config.StackSpotBaseURL,
	}
}

// GetModelName retorna o nome do modelo de linguagem utilizado pelo cliente.
func (c *StackSpotClient) GetModelName() string {
	return config.StackSpotDefaultModel
}

// SendPrompt envia um prompt para o Agente e retorna a resposta direta.
func (c *StackSpotClient) SendPrompt(ctx context.Context, prompt string, history []models.Message, maxTokens int) (string, error) {
	conversationHistory := formatConversationHistory(history)
	fullPrompt := fmt.Sprintf("%sUsuário: %s", conversationHistory, prompt)

	llmResponse, err := utils.Retry(ctx, c.logger, c.maxAttempts, c.backoff, func(ctx context.Context) (string, error) {
		return c.executeWithTokenRetry(ctx, func(token string) (string, error) {
			return c.sendChatRequest(ctx, fullPrompt, token)
		})
	})

	if err != nil {
		c.logger.Error("Erro ao obter a resposta do Agente StackSpot após retries", zap.Error(err))
		return "", err
	}

	return llmResponse, nil
}

// sendChatRequest envia uma requisição de chat para a API do Agente.
func (c *StackSpotClient) sendChatRequest(ctx context.Context, prompt, accessToken string) (string, error) {
	url := fmt.Sprintf("%s/agent/%s/chat", c.baseURL, c.agentID)

	requestBody := map[string]interface{}{
		"streaming":             false,
		"user_prompt":           prompt,
		"stackspot_knowledge":   true, // Habilitado conforme exemplo
		"return_ks_in_response": false,
	}
	jsonValue, err := json.Marshal(requestBody)
	if err != nil {
		return "", fmt.Errorf("erro ao preparar a requisição de chat: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(string(jsonValue)))
	if err != nil {
		return "", fmt.Errorf("erro ao criar a requisição de chat: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := c.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("erro ao fazer a requisição de chat: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("erro ao ler a resposta de chat: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", &utils.APIError{StatusCode: resp.StatusCode, Message: string(bodyBytes)}
	}

	// **BLOCO CORRIGIDO COM BASE NA DOCUMENTAÇÃO**
	var response struct {
		Message    string `json:"message"`
		StopReason string `json:"stop_reason"`
		Tokens     struct {
			User       int `json:"user"`
			Enrichment int `json:"enrichment"`
			Output     int `json:"output"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal(bodyBytes, &response); err != nil {
		return "", fmt.Errorf("erro ao deserializar a resposta do chat: %w", err)
	}

	// Log do consumo de tokens
	c.logger.Debug("Consumo de tokens da StackSpot API",
		zap.Int("user", response.Tokens.User),
		zap.Int("enrichment", response.Tokens.Enrichment),
		zap.Int("output", response.Tokens.Output),
	)

	if response.Message == "" {
		return "", fmt.Errorf("resposta do agente está vazia (stop_reason: %s)", response.StopReason)
	}

	return response.Message, nil
}

func (c *StackSpotClient) executeWithTokenRetry(ctx context.Context, requestFunc func(string) (string, error)) (string, error) {
	token, err := c.tokenManager.GetAccessToken(ctx)
	if err != nil {
		return "", fmt.Errorf("erro ao obter o token: %w", err)
	}

	response, err := requestFunc(token)
	if err != nil {
		if apiErr, ok := err.(*utils.APIError); ok && (apiErr.StatusCode == http.StatusUnauthorized || apiErr.StatusCode == http.StatusForbidden) {
			c.logger.Info("Token inválido ou expirado, renovando...")
			newToken, tokenErr := c.tokenManager.RefreshToken(ctx)
			if tokenErr != nil {
				return "", fmt.Errorf("erro ao renovar o token: %w", tokenErr)
			}
			return requestFunc(newToken)
		}
		return "", err
	}
	return response, nil
}

func formatConversationHistory(history []models.Message) string {
	var conversationBuilder strings.Builder
	for _, msg := range history {
		role := "Usuário"
		if msg.Role == "assistant" {
			role = "Assistente"
		}
		conversationBuilder.WriteString(fmt.Sprintf("%s: %s\n", role, msg.Content))
	}
	return conversationBuilder.String()
}
