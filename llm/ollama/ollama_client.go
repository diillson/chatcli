/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package ollama

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
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/llm/catalog"
	"github.com/diillson/chatcli/llm/client"
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
		model:       strings.ToLower(model),
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
		return "", fmt.Errorf("%s: %w", i18n.T("llm.ollama.prepare_payload"), err)
	}

	// Agora use Retry para encapsular a lógica de requisição e parsing
	response, err := utils.Retry(ctx, c.logger, c.maxAttempts, c.backoff, func(ctx context.Context) (string, error) {
		url := c.baseURL + "/api/chat"

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, utils.NewJSONReader(body))
		if err != nil {
			return "", fmt.Errorf("%s: %w", i18n.T("llm.ollama.create_request"), err)
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()

		bodyBytes, err := io.ReadAll(resp.Body)
		if err != nil {
			return "", fmt.Errorf("%s: %w", i18n.T("llm.ollama.read_response"), err)
		}

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return "", &utils.APIError{StatusCode: resp.StatusCode, Message: utils.SanitizeSensitiveText(string(bodyBytes))}
		}

		var result struct {
			Message struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"message"`
			Done  bool   `json:"done"`
			Error string `json:"error"`
		}

		// Use Unmarshal com bodyBytes
		if err := json.Unmarshal(bodyBytes, &result); err != nil {
			return "", fmt.Errorf("%s: %w", i18n.T("llm.ollama.decode_response"), err)
		}

		if result.Error != "" {
			return "", fmt.Errorf("%s", i18n.T("llm.ollama.api_error", result.Error))
		}
		return result.Message.Content, nil
	})

	if err != nil {
		c.logger.Error(i18n.T("llm.ollama.get_response_error"), zap.Error(err))
		return "", err
	}

	// Aplicar filtro se ENV OLLAMA_FILTER_THINKING = "true"
	if strings.EqualFold(os.Getenv("OLLAMA_FILTER_THINKING"), config.OllamaFilterThinkingDefault) {
		filtered := filterThinking(response)
		if filtered != response {
			c.logger.Debug(i18n.T("llm.ollama.filter_applied"),
				zap.Int("original_length", len(response)),
				zap.Int("filtered_length", len(filtered)))
			return filtered, nil
		}
		c.logger.Debug(i18n.T("llm.ollama.no_thinking_detected"))
	}

	return response, nil
}

// ListModels fetches available models from the Ollama /api/tags endpoint.
func (c *Client) ListModels(ctx context.Context) ([]client.ModelInfo, error) {
	tagsURL := c.baseURL + "/api/tags"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, tagsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", i18n.T("llm.ollama.create_request"), err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", i18n.T("llm.ollama.fetch_models_failed"), err)
	}
	defer func() { _ = resp.Body.Close() }()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", i18n.T("llm.ollama.read_response"), err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s", i18n.T("llm.error.api_error_code", "Ollama", resp.StatusCode, string(bodyBytes)))
	}

	var result struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.Unmarshal(bodyBytes, &result); err != nil {
		return nil, fmt.Errorf("%s: %w", i18n.T("llm.ollama.decode_response"), err)
	}

	var modelList []client.ModelInfo
	for _, m := range result.Models {
		modelList = append(modelList, client.ModelInfo{
			ID:          m.Name,
			DisplayName: m.Name,
			Source:      client.ModelSourceAPI,
		})

		if _, ok := catalog.Resolve(catalog.ProviderOllama, m.Name); !ok {
			catalog.Register(catalog.ModelMeta{
				ID:          m.Name,
				Aliases:     []string{m.Name},
				DisplayName: m.Name,
				Provider:    catalog.ProviderOllama,
			})
		}
	}

	c.logger.Info(i18n.T("llm.info.fetched_models", "Ollama"), zap.Int("count", len(modelList)))
	return modelList, nil
}

// filterThinking remove partes de "pensamento em voz alta" da resposta.
// Retorna a resposta filtrada ou original se não encontrar padrões.
func filterThinking(response string) string {
	// Padrões comuns: tags <thinking>/<think>, ou frases como "Thinking step by step:" / "Reasoning:"
	patterns := []struct {
		re   *regexp.Regexp
		repl string
	}{
		// Remove blocos <think>...</think> ou <thinking>...</thinking>
		{regexp.MustCompile(`(?is)<think(ing)?>.*?</think(ing)?>`), ""},

		// Mantém do "Final Answer:" até o fim (corrige o teste que espera "Final Answer: 42")
		{regexp.MustCompile(`(?is)Thinking step by step:.*?(Final Answer:.*)$`), "$1"},

		// Mantém do "Answer:" até o fim (consistência)
		{regexp.MustCompile(`(?is)Reasoning:.*?(Answer:.*)$`), "$1"},

		// Remove tags escapadas (\<think\>...\</think\>) que alguns modelos retornam
		{regexp.MustCompile(`(?is)\\<think\\>.*?</think>`), ""},
	}

	filtered := response
	changed := false

	for _, p := range patterns {
		newText := p.re.ReplaceAllString(filtered, p.repl)
		if newText != filtered {
			changed = true
			filtered = newText
		}
	}

	// Se não mudou nada, retorna exatamente o original
	if !changed {
		return response
	}

	// Limpa apenas quando houve remoção/substituição
	filtered = strings.TrimSpace(filtered)
	filtered = regexp.MustCompile(`(?m)^\s*\n`).ReplaceAllString(filtered, "")

	return filtered
}
