/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
 */
package ollama

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

type Client struct {
	baseURL     string
	model       string
	logger      *zap.Logger
	httpClient  *http.Client
	maxAttempts int
	backoff     time.Duration
}

// NewClient cria uma nova instância de Client para Ollama.
// Agora sem fallback interno: usa apenas os params passados (vindos do manager/ENVs).
func NewClient(baseURL, model string, logger *zap.Logger, maxAttempts int, backoff time.Duration) *Client {
	if baseURL == "" {
		baseURL = config.OllamaDefaultBaseURL
	}
	if model == "" {
		model = config.DefaultOllamaModel
	}

	return &Client{
		baseURL:     strings.TrimRight(baseURL, "/"),
		model:       model,
		logger:      logger,
		httpClient:  utils.NewHTTPClient(logger, 300*time.Second),
		maxAttempts: maxAttempts,
		backoff:     backoff,
	}
}

func (c *Client) GetModelName() string {
	// Caso não tenha cadastro no catálogo, retorna o próprio ID
	return catalog.GetDisplayName(catalog.ProviderOllama, c.model)
}

func (c *Client) getMaxTokens() int {
	if v := os.Getenv("OLLAMA_MAX_TOKENS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return catalog.GetMaxTokens(catalog.ProviderOllama, c.model, 0)
}

func (c *Client) SendPrompt(ctx context.Context, prompt string, history []models.Message, maxTokens int) (string, error) {
	effectiveMaxTokens := maxTokens
	if effectiveMaxTokens <= 0 {
		effectiveMaxTokens = c.getMaxTokens() // Fallback se nada for passado
	}

	// Monta mensagens a partir do histórico, sem duplicar o prompt (mesma lógica dos outros clientes)
	var msgs []map[string]string
	for _, m := range history {
		role := strings.ToLower(strings.TrimSpace(m.Role))
		switch role {
		case "system", "user", "assistant":
		default:
			role = "user"
		}
		msgs = append(msgs, map[string]string{"role": role, "content": m.Content})
	}

	if len(history) == 0 || history[len(history)-1].Role != "user" || history[len(history)-1].Content != prompt {
		if strings.TrimSpace(prompt) != "" {
			msgs = append(msgs, map[string]string{"role": "user", "content": prompt})
		}
	}

	// Payload /api/chat
	reqBody := map[string]interface{}{
		"model":    c.model,
		"messages": msgs,
		"stream":   false,
		"options": map[string]interface{}{
			"num_predict": effectiveMaxTokens,
			// "temperature": 0.7,
			// "num_ctx": 8192,
		},
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("erro ao preparar payload do Ollama: %w", err)
	}

	// Agora use Retry para encapsular a lógica de requisição e parsing
	response, err := utils.Retry(ctx, c.logger, c.maxAttempts, c.backoff, func(ctx context.Context) (string, error) {
		url := c.baseURL + "/api/chat"

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, utils.NewJSONReader(body))
		if err != nil {
			return "", fmt.Errorf("erro criando requisição Ollama: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()

		bodyBytes, err := io.ReadAll(resp.Body)
		if err != nil {
			return "", fmt.Errorf("erro ao ler resposta do Ollama: %w", err)
		}

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return "", &utils.APIError{StatusCode: resp.StatusCode, Message: string(bodyBytes)}
		}

		var result struct {
			Message struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"message"`
			Done  bool   `json:"done"`
			Error string `json:"error"`
		}

		// CORREÇÃO AQUI: Use Unmarshal com bodyBytes
		if err := json.Unmarshal(bodyBytes, &result); err != nil {
			return "", fmt.Errorf("erro ao decodificar resposta do Ollama: %w", err)
		}

		if result.Error != "" {
			return "", fmt.Errorf("erro Ollama: %s", result.Error)
		}
		return result.Message.Content, nil
	})

	if err != nil {
		c.logger.Error("Erro ao obter resposta do Ollama após retries", zap.Error(err))
		return "", err
	}

	return response, nil
}
