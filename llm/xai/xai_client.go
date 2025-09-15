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
	if maxAttempts <= 0 {
		maxAttempts = config.OpenAIDefaultMaxAttempts
	}
	if backoff <= 0 {
		backoff = config.OpenAIDefaultBackoff
	}

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
func (c *XAIClient) SendPrompt(ctx context.Context, prompt string, history []models.Message) (string, error) {
	maxTokens := c.getMaxTokens()

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
		"max_tokens": maxTokens,
	}

	jsonValue, err := json.Marshal(payload)
	if err != nil {
		c.logger.Error("Erro ao marshalizar o payload para xAI", zap.Error(err))
		return "", fmt.Errorf("erro ao preparar a requisição: %w", err)
	}

	var backoff = c.backoff
	for attempt := 1; attempt <= c.maxAttempts; attempt++ {
		resp, err := c.sendRequest(ctx, jsonValue)
		if err != nil {
			if utils.IsTemporaryError(err) {
				c.logger.Warn("Erro temporário ao chamar API da xAI", zap.Int("attempt", attempt), zap.Error(err), zap.Duration("backoff", backoff))
				if attempt < c.maxAttempts {
					time.Sleep(backoff)
					backoff *= 2
					continue
				}
			}
			c.logger.Error("Erro ao fazer a requisição para xAI", zap.Error(err))
			return "", fmt.Errorf("erro ao fazer a requisição para xAI: %w", err)
		}

		response, err := c.processResponse(resp)
		if err != nil {
			c.logger.Error("Erro ao processar a resposta da xAI", zap.Error(err))
			return "", err
		}
		return response, nil
	}

	return "", fmt.Errorf("falha ao obter resposta da xAI após %d tentativas", c.maxAttempts)
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
		return "", fmt.Errorf("erro ao ler a resposta da xAI: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("erro na requisição à xAI: status %d, resposta: %s", resp.StatusCode, string(bodyBytes))
	}

	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}

	if err := json.Unmarshal(bodyBytes, &result); err != nil {
		return "", fmt.Errorf("erro ao decodificar a resposta da xAI: %w", err)
	}

	if len(result.Choices) == 0 {
		return "", errors.New("nenhuma resposta recebida da xAI")
	}

	return result.Choices[0].Message.Content, nil
}
