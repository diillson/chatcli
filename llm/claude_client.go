package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/utils"
	"go.uber.org/zap"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	claudeAIAPIURL = "https://api.anthropic.com/v1/messages"
)

// ClaudeClient é uma estrutura que contém o cliente de ClaudeAI com suas configurações
type ClaudeClient struct {
	apiKey string
	model  string
	logger *zap.Logger
	client *http.Client
}

// NewClaudeClient cria um novo cliente ClaudeAI com configurações personalizáveis
func NewClaudeClient(apiKey string, model string, logger *zap.Logger) *ClaudeClient {
	// Usar o transporte HTTP com logging
	httpClient := utils.NewHTTPClient(logger, 300*time.Second)

	return &ClaudeClient{
		apiKey: apiKey,
		model:  model,
		logger: logger,
		client: httpClient,
	}
}

// GetModelName retorna o nome do modelo configurado para ClaudeAI
func (c *ClaudeClient) GetModelName() string {
	if c.model == "claude-3-5-sonnet-20241022" {
		return "claude 3.5 sonnet"
	}
	return c.model
}

// SendPrompt monta a requisição com o histórico e a envia para a ClaudeAI, retornando a resposta formatada
func (c *ClaudeClient) SendPrompt(ctx context.Context, prompt string, history []models.Message) (string, error) {
	messages := c.buildMessages(prompt, history)

	reqBody := map[string]interface{}{
		"model":      c.model,
		"max_tokens": 8192,
		"messages":   messages,
	}
	reqJSON, _ := json.Marshal(reqBody)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, claudeAIAPIURL, strings.NewReader(string(reqJSON)))
	if err != nil {
		c.logger.Error("Erro ao criar a requisição de prompt", zap.Error(err))
		return "", fmt.Errorf("erro ao criar a requisição: %w", err)
	}

	req.Header.Add("Content-Type", "application/json")
	req.Header.Add("x-api-key", c.apiKey)
	req.Header.Add("anthropic-version", "2023-06-01")

	resp, err := c.client.Do(req)
	if err != nil {
		c.logger.Error("Erro ao fazer a requisição de prompt", zap.Error(err))
		return "", fmt.Errorf("erro ao fazer a requisição: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		c.logger.Error("Erro ao obter resposta da ClaudeAI", zap.Int("status", resp.StatusCode), zap.String("body", string(body)))
		return "", fmt.Errorf("erro ao obter resposta da ClaudeAI: status %d, body %s", resp.StatusCode, string(body))
	}

	return c.parseResponse(resp)
}

// buildMessages monta o histórico de mensagens para incluir na requisição
func (c *ClaudeClient) buildMessages(prompt string, history []models.Message) []map[string]string {
	messages := make([]map[string]string, len(history))

	// Processa o histórico, garantindo que role e content estejam bem definidos
	for i, msg := range history {
		role := "user"
		if msg.Role == "assistant" {
			role = "assistant"
		}
		messages[i] = map[string]string{"role": role, "content": msg.Content}
	}

	// Adiciona a mensagem atual do usuário ao final
	messages = append(messages, map[string]string{"role": "user", "content": prompt})

	return messages
}

// parseResponse decodifica e processa a resposta da ClaudeAI
func (c *ClaudeClient) parseResponse(resp *http.Response) (string, error) {
	var result struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		c.logger.Error("Erro ao decodificar a resposta da ClaudeAI", zap.Error(err))
		return "", fmt.Errorf("erro ao decodificar a resposta: %w", err)
	}

	var responseText string
	for _, content := range result.Content {
		if content.Type == "text" {
			responseText += content.Text
		}
	}

	if responseText == "" {
		c.logger.Error("Nenhum conteúdo de texto encontrado na resposta da ClaudeAI")
		return "", fmt.Errorf("erro ao obter a resposta da ClaudeAI")
	}

	return responseText, nil
}
