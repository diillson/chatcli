/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package openai

import (
	"bufio"
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
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/llm/catalog"
	"github.com/diillson/chatcli/llm/client"

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
	usageState  client.UsageState
}

// LastUsage returns the token usage from the most recent API call.
func (c *OpenAIClient) LastUsage() *models.UsageInfo { return c.usageState.LastUsage() }

// LastStopReason returns the stop reason from the most recent API call.
func (c *OpenAIClient) LastStopReason() string { return c.usageState.LastStopReason() }

// NewOpenAIClient cria uma nova instância de OpenAIClient.
func NewOpenAIClient(apiKey, model string, logger *zap.Logger, maxAttempts int, backoff time.Duration) *OpenAIClient {
	httpClient := utils.NewHTTPClient(logger, 900*time.Second)

	return &OpenAIClient{
		apiKey:      apiKey,
		model:       strings.ToLower(model),
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
		c.logger.Error(i18n.T("llm.error.marshal_payload"), zap.Error(err))
		return "", fmt.Errorf("%s: %w", i18n.T("llm.error.prepare_request"), err)
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
		c.logger.Error(i18n.T("llm.error.create_request"), zap.Error(err))
		return nil, fmt.Errorf("%s: %w", i18n.T("llm.error.create_request"), err)
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
		c.logger.Error(i18n.T("llm.error.read_response_for", "OpenAI"), zap.Error(err))
		return "", fmt.Errorf("%s: %w", i18n.T("llm.error.read_response_for", "OpenAI"), err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", &utils.APIError{StatusCode: resp.StatusCode, Message: utils.SanitizeSensitiveText(string(bodyBytes))}
	}

	var result map[string]interface{}
	if err := json.Unmarshal(bodyBytes, &result); err != nil {
		c.logger.Error(i18n.T("llm.error.decode_response_for", "OpenAI"), zap.Error(err))
		return "", fmt.Errorf("%s: %w", i18n.T("llm.error.decode_response_for", "OpenAI"), err)
	}

	// Extract usage and stop reason
	if usage := client.ParseOpenAIUsage(result); usage != nil {
		c.usageState.StoreUsage(usage)
	}
	c.usageState.StoreStopReason(client.ParseOpenAIFinishReason(result))

	choices, ok := result["choices"].([]interface{})
	if !ok || len(choices) == 0 {
		c.logger.Error(i18n.T("llm.error.no_response", "OpenAI"), zap.Any("resultado", result))
		return "", fmt.Errorf("%s", i18n.T("llm.error.no_response", "OpenAI"))
	}

	firstChoice, ok := choices[0].(map[string]interface{})
	if !ok {
		c.logger.Error(i18n.T("llm.error.unexpected_format", "OpenAI"), zap.Any("choice", choices[0]))
		return "", fmt.Errorf("%s", i18n.T("llm.error.unexpected_format", "OpenAI"))
	}

	message, ok := firstChoice["message"].(map[string]interface{})
	if !ok {
		c.logger.Error(i18n.T("llm.error.missing_message_field", "OpenAI"), zap.Any("choice", firstChoice))
		return "", fmt.Errorf("%s", i18n.T("llm.error.missing_message_field", "OpenAI"))
	}

	content, ok := message["content"].(string)
	if !ok {
		c.logger.Error(i18n.T("llm.error.invalid_message_content"), zap.Any("content", message["content"]))
		return "", fmt.Errorf("%s", i18n.T("llm.error.invalid_message_content"))
	}

	return content, nil
}

// ListModels fetches available models from the OpenAI /v1/models endpoint.
func (c *OpenAIClient) ListModels(ctx context.Context) ([]client.ModelInfo, error) {
	// Derive base URL from the chat completions URL
	apiURL := utils.GetEnvOrDefault("OPENAI_API_URL", config.OpenAIAPIURL)
	modelsURL := strings.TrimSuffix(apiURL, "/chat/completions") + "/models"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, modelsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", i18n.T("llm.error.create_request"), err)
	}
	req.Header.Set("Authorization", "Bearer "+auth.StripAuthPrefix(c.apiKey))

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", i18n.T("llm.error.request_failed", "OpenAI"), err)
	}
	defer func() { _ = resp.Body.Close() }()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", i18n.T("llm.error.read_response_for", "OpenAI"), err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("OpenAI /models returned %d: %s", resp.StatusCode, utils.SanitizeSensitiveText(string(bodyBytes)))
	}

	var result struct {
		Data []struct {
			ID      string `json:"id"`
			OwnedBy string `json:"owned_by"`
		} `json:"data"`
	}
	if err := json.Unmarshal(bodyBytes, &result); err != nil {
		return nil, fmt.Errorf("%s: %w", i18n.T("llm.error.decode_response_for", "OpenAI"), err)
	}

	var models []client.ModelInfo
	for _, m := range result.Data {
		// Filter to chat-capable models (gpt, o1, o3, o4, chatgpt)
		id := strings.ToLower(m.ID)
		if !strings.HasPrefix(id, "gpt-") && !strings.HasPrefix(id, "o1-") &&
			!strings.HasPrefix(id, "o3-") && !strings.HasPrefix(id, "o4-") &&
			!strings.HasPrefix(id, "chatgpt-") {
			continue
		}
		models = append(models, client.ModelInfo{
			ID:          m.ID,
			DisplayName: m.ID,
			Source:      client.ModelSourceAPI,
		})

		if _, ok := catalog.Resolve(catalog.ProviderOpenAI, m.ID); !ok {
			catalog.Register(catalog.ModelMeta{
				ID:           m.ID,
				Aliases:      []string{m.ID},
				DisplayName:  m.ID,
				Provider:     catalog.ProviderOpenAI,
				PreferredAPI: catalog.APIChatCompletions,
			})
		}
	}

	c.logger.Info(i18n.T("llm.info.fetched_models", "OpenAI"), zap.Int("count", len(models)))
	return models, nil
}

// SupportsStreaming returns true — OpenAI supports SSE streaming.
func (c *OpenAIClient) SupportsStreaming() bool {
	return true
}

// SendPromptStream sends a prompt and returns a channel of streaming chunks.
func (c *OpenAIClient) SendPromptStream(ctx context.Context, prompt string, history []models.Message, maxTokens int) (<-chan client.StreamChunk, error) {
	effectiveMaxTokens := maxTokens
	if effectiveMaxTokens <= 0 {
		effectiveMaxTokens = c.getMaxTokens()
	}

	messages := []map[string]string{}
	for _, msg := range history {
		role := strings.ToLower(strings.TrimSpace(msg.Role))
		switch role {
		case "system", "user", "assistant":
		default:
			role = "user"
		}
		messages = append(messages, map[string]string{"role": role, "content": msg.Content})
	}
	if len(history) == 0 || history[len(history)-1].Role != "user" || history[len(history)-1].Content != prompt {
		if strings.TrimSpace(prompt) != "" {
			messages = append(messages, map[string]string{"role": "user", "content": prompt})
		}
	}

	payload := map[string]interface{}{
		"model":      c.model,
		"messages":   messages,
		"max_tokens": effectiveMaxTokens,
		"stream":     true,
	}

	jsonValue, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal stream request: %w", err)
	}

	resp, err := c.sendRequest(ctx, jsonValue)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return nil, &utils.APIError{StatusCode: resp.StatusCode, Message: utils.SanitizeSensitiveText(string(raw))}
	}

	chunks := make(chan client.StreamChunk, 64)

	go func() {
		defer close(chunks)
		defer func() { _ = resp.Body.Close() }()

		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			data := strings.TrimPrefix(line, "data: ")
			if data == "[DONE]" {
				chunks <- client.StreamChunk{
					Done:       true,
					Usage:      c.usageState.LastUsage(),
					StopReason: c.usageState.LastStopReason(),
				}
				return
			}

			var evt map[string]interface{}
			if err := json.Unmarshal([]byte(data), &evt); err != nil {
				continue
			}

			// Extract text delta
			if choices, ok := evt["choices"].([]interface{}); ok && len(choices) > 0 {
				if choice, ok := choices[0].(map[string]interface{}); ok {
					if delta, ok := choice["delta"].(map[string]interface{}); ok {
						if content, ok := delta["content"].(string); ok && content != "" {
							chunks <- client.StreamChunk{Text: content}
						}
					}
					if reason, ok := choice["finish_reason"].(string); ok && reason != "" {
						c.usageState.StoreStopReason(reason)
					}
				}
			}

			// Extract usage (some OpenAI models include it in the final chunk)
			if usage := client.ParseOpenAIUsage(evt); usage != nil {
				c.usageState.StoreUsage(usage)
			}
		}

		// Scanner finished without [DONE]
		chunks <- client.StreamChunk{
			Done:       true,
			Usage:      c.usageState.LastUsage(),
			StopReason: c.usageState.LastStopReason(),
		}
	}()

	return chunks, nil
}
