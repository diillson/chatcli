/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
 */
package claudeai

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

	"github.com/diillson/chatcli/config"
	"github.com/diillson/chatcli/llm/catalog"
	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/utils"
	"go.uber.org/zap"
)

// ClaudeClient é uma estrutura que contém o cliente de ClaudeAI com suas configurações
type ClaudeClient struct {
	apiKey      string
	model       string
	logger      *zap.Logger
	client      *http.Client
	maxAttempts int
	backoff     time.Duration
	apiURL      string
}

// NewClaudeClient cria um novo cliente ClaudeAI com configurações personalizáveis
func NewClaudeClient(apiKey string, model string, logger *zap.Logger, maxAttempts int, backoff time.Duration) *ClaudeClient {
	// Usar o transporte HTTP com logging
	httpClient := utils.NewHTTPClient(logger, 900*time.Second)
	if maxAttempts <= 0 {
		maxAttempts = config.ClaudeAIDefaultMaxAttempts
	}
	if backoff <= 0 {
		backoff = config.ClaudeAIDefaultBackoff
	}

	return &ClaudeClient{
		apiKey:      apiKey,
		model:       model,
		logger:      logger,
		client:      httpClient,
		maxAttempts: maxAttempts,
		backoff:     backoff,
		apiURL:      config.ClaudeAIAPIURL,
	}
}

// GetModelName retorna o nome amigável do modelo ClaudeAI
func (c *ClaudeClient) GetModelName() string {
	return catalog.GetDisplayName(catalog.ProviderClaudeAI, c.model)
}

// SendPrompt com exponential backoff
func (c *ClaudeClient) SendPrompt(ctx context.Context, prompt string, history []models.Message) (string, error) {
	// Constrói mensagens e system a partir do history, sem duplicar o prompt
	messages, systemStr := c.buildMessagesAndSystem(prompt, history)

	// Obter max_tokens da variável de ambiente ou usar o padrão
	maxTokens := config.ClaudeAIDefaultMaxTokens
	if tokenStr := os.Getenv("CLAUDEAI_MAX_TOKENS"); tokenStr != "" {
		if parsedTokens, err := strconv.Atoi(tokenStr); err == nil && parsedTokens > 0 {
			maxTokens = parsedTokens
			c.logger.Debug("Usando max_tokens personalizado", zap.Int("max_tokens", maxTokens))
		}
	}

	// Configuração para requisição
	reqBody := map[string]interface{}{
		"model":      c.model,
		"max_tokens": maxTokens,
		"messages":   messages,
	}

	// Incluir "system" top-level se houver
	if strings.TrimSpace(systemStr) != "" {
		reqBody["system"] = systemStr
	}

	jsonValue, err := json.Marshal(reqBody)
	if err != nil {
		c.logger.Error("Erro ao marshalizar o payload", zap.Error(err))
		return "", fmt.Errorf("erro ao preparar a requisição: %w", err)
	}

	// Implementação do backoff exponencial (mantida)
	var backoff = c.backoff

	for attempt := 1; attempt <= c.maxAttempts; attempt++ {
		// Cria uma nova requisição para cada tentativa
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiURL, strings.NewReader(string(jsonValue)))
		if err != nil {
			c.logger.Error("Erro ao criar a requisição de prompt", zap.Error(err))
			return "", fmt.Errorf("erro ao criar a requisição: %w", err)
		}

		req.Header.Add("Content-Type", "application/json")
		req.Header.Add("x-api-key", c.apiKey)

		apiVersion := os.Getenv("CLAUDEAI_API_VERSION")
		if apiVersion == "" {
			apiVersion = catalog.GetAnthropicAPIVersion(c.model)
			if apiVersion == "" {
				apiVersion = config.ClaudeAIAPIVersionDefault
			}
		}
		req.Header.Add("anthropic-version", apiVersion)

		// Executa a requisição
		resp, err := c.client.Do(req)

		// Verifica erros na requisição
		if err != nil {
			if utils.IsTemporaryError(err) {
				c.logger.Warn("Erro temporário ao chamar Claude AI",
					zap.Int("attempt", attempt),
					zap.Error(err),
					zap.Duration("backoff", backoff),
				)

				if attempt < c.maxAttempts {
					time.Sleep(backoff)
					backoff *= 2 // Backoff exponencial
					continue
				}
			}

			c.logger.Error("Erro ao fazer a requisição para Claude AI", zap.Error(err))
			return "", fmt.Errorf("erro ao fazer a requisição para Claude AI: %w", err)
		}

		// Verifica o status code da resposta
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()

			// Trata especificamente erros de rate limit
			if resp.StatusCode == http.StatusTooManyRequests {
				c.logger.Warn("Rate limit excedido na API da Claude AI",
					zap.Int("attempt", attempt),
					zap.Int("status", resp.StatusCode),
					zap.String("body", string(body)),
				)

				if attempt < c.maxAttempts {
					time.Sleep(backoff)
					backoff *= 2 // Backoff exponencial
					continue
				}
			}

			c.logger.Error("Erro ao obter resposta da Claude AI",
				zap.Int("status", resp.StatusCode),
				zap.String("body", string(body)))
			return "", fmt.Errorf("erro ao obter resposta da Claude AI: status %d, body %s",
				resp.StatusCode, string(body))
		}

		// Se chegou aqui, a requisição foi bem-sucedida
		responseText, err := c.parseResponse(resp)
		if err != nil {
			c.logger.Error("Erro ao processar a resposta da Claude AI", zap.Error(err))
			return "", err
		}

		return responseText, nil
	}

	return "", fmt.Errorf("falha ao obter resposta da Claude AI após %d tentativas", c.maxAttempts)
}

// buildMessagesAndSystem monta o array de mensagens (user/assistant) e agrega
// as mensagens "system" em um único system string top-level.
// NÃO duplica o prompt: o ChatCLI já insere a última mensagem do usuário no history.
func (c *ClaudeClient) buildMessagesAndSystem(prompt string, history []models.Message) ([]map[string]string, string) {
	var messages []map[string]string
	var systemParts []string

	for _, msg := range history {
		switch strings.ToLower(strings.TrimSpace(msg.Role)) {
		case "assistant":
			messages = append(messages, map[string]string{
				"role":    "assistant",
				"content": msg.Content,
			})
		case "system":
			systemParts = append(systemParts, msg.Content)
		default: // "user" e qualquer outro vira "user"
			messages = append(messages, map[string]string{
				"role":    "user",
				"content": msg.Content,
			})
		}
	}

	// Fallback: se por algum motivo o history não tem o último turno do user,
	// adicionar o prompt como user.
	if len(history) == 0 || history[len(history)-1].Role != "user" || history[len(history)-1].Content != prompt {
		if strings.TrimSpace(prompt) != "" {
			messages = append(messages, map[string]string{
				"role":    "user",
				"content": prompt,
			})
		}
	}

	var systemStr string
	if len(systemParts) > 0 {
		systemStr = strings.Join(systemParts, "\n\n")
	}

	return messages, systemStr
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
