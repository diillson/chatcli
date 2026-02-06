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
	"regexp"
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

// NewClaudeClient cria um novo cliente ClaudeAI com configurações personalizáveis.
func NewClaudeClient(apiKey string, model string, logger *zap.Logger, maxAttempts int, backoff time.Duration) *ClaudeClient {
	httpClient := utils.NewHTTPClient(logger, 900*time.Second)
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

// getMaxTokens obtém o limite de tokens configurado
func (c *ClaudeClient) getMaxTokens() int {
	if tokenStr := os.Getenv("CLAUDEAI_MAX_TOKENS"); tokenStr != "" {
		if parsedTokens, err := strconv.Atoi(tokenStr); err == nil && parsedTokens > 0 {
			c.logger.Debug("Usando max_tokens personalizado", zap.Int("max_tokens", parsedTokens))
			return parsedTokens
		}
	}
	return catalog.GetMaxTokens(catalog.ProviderClaudeAI, c.model, 0)
}

// SendPrompt com exponential backoff usando utils.Retry
func (c *ClaudeClient) SendPrompt(ctx context.Context, prompt string, history []models.Message, maxTokens int) (string, error) {
	effectiveMaxTokens := maxTokens
	if effectiveMaxTokens <= 0 {
		effectiveMaxTokens = c.getMaxTokens()
	}

	messages, systemStr := c.buildMessagesAndSystem(prompt, history)

	if strings.HasPrefix(c.apiKey, "oauth:") {
		systemStr = "You are Claude Code, Anthropic's official CLI for Claude.\n" + systemStr
	}

	reqBody := map[string]interface{}{
		"model":      c.model,
		"max_tokens": effectiveMaxTokens,
		"messages":   messages,
	}

	if strings.HasPrefix(c.apiKey, "oauth:") {
		systemStr = "You are Claude Code, Anthropic's official CLI for Claude.\n" + systemStr
	}

	if strings.HasPrefix(c.apiKey, "oauth:") {
		systemStr = "You are Claude Code, Anthropic's official CLI for Claude.\n" + systemStr
	}

	if strings.HasPrefix(c.apiKey, "oauth:") {
		systemStr = "You are Claude Code, Anthropic's official CLI for Claude.\n" + systemStr
	}

	if strings.TrimSpace(systemStr) != "" {
		reqBody["system"] = systemStr
	}

	jsonValue, err := json.Marshal(reqBody)
	if err != nil {
		c.logger.Error("Erro ao marshalizar o payload", zap.Error(err))
		return "", fmt.Errorf("erro ao preparar a requisição: %w", err)
	}

	responseText, err := utils.Retry(ctx, c.logger, c.maxAttempts, c.backoff, func(ctx context.Context) (string, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiURL, strings.NewReader(string(jsonValue)))
		if err != nil {
			return "", fmt.Errorf("erro ao criar a requisição: %w", err)
		}

		enable1mtokens := os.Getenv("CLAUDEAI_1MTOKENS_SONNET") == "true"

		req.Header.Add("Content-Type", "application/json")
		if strings.HasPrefix(c.apiKey, "oauth:") {
			req.Header.Add("Authorization", "Bearer "+strings.TrimPrefix(c.apiKey, "oauth:"))
			req.Header.Add("anthropic-beta", "oauth-2025-04-20")
			req.Header.Set("User-Agent", "claude-code")
			req.Header.Add("anthropic-client", "claude-code")
		} else if strings.HasPrefix(c.apiKey, "token:") {
			req.Header.Add("Authorization", "Bearer "+strings.TrimPrefix(c.apiKey, "token:"))
		} else if strings.HasPrefix(c.apiKey, "apikey:") {
			req.Header.Add("x-api-key", strings.TrimPrefix(c.apiKey, "apikey:"))
		} else {
			req.Header.Add("x-api-key", c.apiKey)
		}
		req.Header.Add("anthropic-version", catalog.GetAnthropicAPIVersion(c.model))
		if enable1mtokens && isClaudeSonnet(c.model) {
			req.Header.Add("anthropic-beta", "context-1m-2025-08-07")
		}

		resp, err := c.client.Do(req)
		if err != nil {
			return "", err
		}

		return c.processResponse(resp)
	})

	if err != nil {
		c.logger.Error("Erro ao obter resposta da Claude AI após retries", zap.Error(err))
		return "", err
	}

	return responseText, nil
}

// helper beta 1m tokens sonnet model
func isClaudeSonnet(model string) bool {
	var claudeSonnetRe = regexp.MustCompile(`^claude-.*sonnet.*$`)
	return claudeSonnetRe.MatchString(model)
}

func (c *ClaudeClient) processResponse(resp *http.Response) (string, error) {
	defer func() { _ = resp.Body.Close() }()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		c.logger.Error("Erro ao ler a resposta da ClaudeAI", zap.Error(err))
		return "", fmt.Errorf("erro ao ler a resposta: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", &utils.APIError{StatusCode: resp.StatusCode, Message: string(bodyBytes)}
	}

	var result struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}

	if err := json.Unmarshal(bodyBytes, &result); err != nil {
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

func (c *ClaudeClient) buildMessagesAndSystem(prompt string, history []models.Message) ([]map[string]string, string) {
	var messages []map[string]string
	var systemParts []string

	for _, msg := range history {
		switch strings.ToLower(strings.TrimSpace(msg.Role)) {
		case "assistant":
			messages = append(messages, map[string]string{"role": "assistant", "content": msg.Content})
		case "system":
			systemParts = append(systemParts, msg.Content)
		default:
			messages = append(messages, map[string]string{"role": "user", "content": msg.Content})
		}
	}

	if len(history) == 0 || history[len(history)-1].Role != "user" || history[len(history)-1].Content != prompt {
		if strings.TrimSpace(prompt) != "" {
			messages = append(messages, map[string]string{"role": "user", "content": prompt})
		}
	}

	return messages, strings.Join(systemParts, "\n\n")
}
