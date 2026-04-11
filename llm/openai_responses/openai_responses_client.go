/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package openai_responses

import (
	"bufio"
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

	"github.com/diillson/chatcli/auth"
	"github.com/diillson/chatcli/config"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/llm/catalog"
	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/utils"
	"go.uber.org/zap"
)

// OpenAIResponsesClient implementa o cliente para a API /v1/responses
type OpenAIResponsesClient struct {
	apiKey      string
	model       string
	logger      *zap.Logger
	httpClient  *http.Client
	maxAttempts int
	backoff     time.Duration
	usageState  client.UsageState
}

// LastUsage returns the token usage from the most recent API call.
func (c *OpenAIResponsesClient) LastUsage() *models.UsageInfo { return c.usageState.LastUsage() }

// LastStopReason returns the stop reason from the most recent API call.
func (c *OpenAIResponsesClient) LastStopReason() string { return c.usageState.LastStopReason() }

// NewOpenAIResponsesClient cria uma nova instância de OpenAIResponsesClient.
// Agora sem fallback interno: usa apenas os params passados (vindos do manager/ENVs).
func NewOpenAIResponsesClient(apiKey, model string, logger *zap.Logger, maxAttempts int, backoff time.Duration) *OpenAIResponsesClient {
	var httpClient *http.Client
	if strings.HasPrefix(apiKey, "oauth:") {
		// OAuth requests go to chatgpt.com which requires a browser-like TLS fingerprint
		httpClient = utils.NewHTTPClientWithTransport(logger, 900*time.Second, utils.NewChromeTLSTransport())
	} else {
		httpClient = utils.NewHTTPClient(logger, 900*time.Second)
	}
	return &OpenAIResponsesClient{
		apiKey:      apiKey,
		model:       strings.ToLower(model),
		logger:      logger,
		httpClient:  httpClient,
		maxAttempts: maxAttempts,
		backoff:     backoff,
	}
}

func (c *OpenAIResponsesClient) GetModelName() string {
	return catalog.GetDisplayName(catalog.ProviderOpenAI, c.model)
}

func (c *OpenAIResponsesClient) getMaxTokens() int {
	if tokenStr := os.Getenv("OPENAI_MAX_TOKENS"); tokenStr != "" {
		if parsedTokens, err := strconv.Atoi(tokenStr); err == nil && parsedTokens > 0 {
			c.logger.Debug("Usando OPENAI_MAX_TOKENS personalizado", zap.Int("max_tokens", parsedTokens))
			return parsedTokens
		}
	}
	// Fallback para catálogo
	return catalog.GetMaxTokens(catalog.ProviderOpenAI, c.model, 0)
}

func (c *OpenAIResponsesClient) SendPrompt(ctx context.Context, prompt string, history []models.Message, maxTokens int) (string, error) {
	effectiveMaxTokens := maxTokens
	if effectiveMaxTokens <= 0 {
		effectiveMaxTokens = c.getMaxTokens()
	}

	isOAuth := strings.HasPrefix(c.apiKey, "oauth:")

	var reqBody map[string]interface{}

	if isOAuth {
		// ChatGPT backend requires "instructions" and structured input
		instructions, conversationInput := buildOAuthPayload(history, prompt)
		reqBody = map[string]interface{}{
			"model":        c.model,
			"instructions": instructions,
			"input":        conversationInput,
			"store":        false,
			"stream":       true,
		}
	} else {
		input := buildTextFromHistory(history, "")
		// Fallback: se o history não tem o último turno do user (edge-case),
		// anexa o prompt no final do input.
		if len(history) == 0 || history[len(history)-1].Role != "user" || history[len(history)-1].Content != prompt {
			if strings.TrimSpace(prompt) != "" {
				if strings.TrimSpace(input) != "" {
					input += "\n"
				}
				input += "User: " + prompt
			}
		}
		reqBody = map[string]interface{}{
			"model":             c.model,
			"input":             input,
			"max_output_tokens": effectiveMaxTokens,
		}
	}

	jsonValue, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("%s: %w", i18n.T("llm.responses.prepare_payload"), err)
	}

	// Retry para encapsular a lógica de requisição e parsing
	response, err := utils.Retry(ctx, c.logger, c.maxAttempts, c.backoff, func(ctx context.Context) (string, error) {
		resp, err := c.sendRequest(ctx, jsonValue)
		if err != nil {
			return "", err
		}
		if isOAuth {
			return c.processStreamResponse(resp)
		}
		return c.processResponse(resp)
	})

	if err != nil {
		c.logger.Error(i18n.T("llm.responses.get_response_error"), zap.Error(err))
		return "", err
	}

	return response, nil
}

func (c *OpenAIResponsesClient) sendRequest(ctx context.Context, body []byte) (*http.Response, error) {
	// OAuth tokens use the ChatGPT backend; API keys use the platform API
	var apiURL string
	if strings.HasPrefix(c.apiKey, "oauth:") {
		apiURL = utils.GetEnvOrDefault("OPENAI_RESPONSES_API_URL", config.OpenAIOAuthResponsesURL)
	} else {
		apiURL = utils.GetEnvOrDefault("OPENAI_RESPONSES_API_URL", config.OpenAIResponsesAPIURL)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, utils.NewJSONReader(body))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", i18n.T("llm.responses.create_request"), err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+auth.StripAuthPrefix(c.apiKey))

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	return resp, nil
}

func (c *OpenAIResponsesClient) processResponse(resp *http.Response) (string, error) {
	defer func() { _ = resp.Body.Close() }()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("%s: %w", i18n.T("llm.responses.read_response"), err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", &utils.APIError{StatusCode: resp.StatusCode, Message: utils.SanitizeSensitiveText(string(bodyBytes))}
	}

	// Estrutura de decodificação mais detalhada para capturar todos os casos
	var response struct {
		Status            string `json:"status"`
		OutputText        string `json:"output_text"`
		IncompleteDetails *struct {
			Reason string `json:"reason"`
		} `json:"incomplete_details"`
		Output []struct {
			Type    string `json:"type"` // "message" ou "reasoning"
			Content []struct {
				Type string `json:"type"` // "output_text"
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}

	if err := json.Unmarshal(bodyBytes, &response); err != nil {
		return "", fmt.Errorf("%s: %w", i18n.T("llm.responses.decode_response"), err)
	}

	// Extract usage and stop reason from the Responses API format
	var rawResult map[string]interface{}
	if err := json.Unmarshal(bodyBytes, &rawResult); err == nil {
		if usage := client.ParseOpenAIUsage(rawResult); usage != nil {
			c.usageState.StoreUsage(usage)
		}
		// Responses API uses "status" instead of choices[].finish_reason
		if status, ok := rawResult["status"].(string); ok && status != "" {
			c.usageState.StoreStopReason(status)
		}
	}

	// Tentar extrair do caminho simples primeiro (comum em mocks e respostas diretas)
	if response.OutputText != "" {
		return response.OutputText, nil
	}

	// 1. Verificar se a API retornou um erro explícito no corpo
	if response.Error != nil && response.Error.Message != "" {
		c.logger.Error(i18n.T("llm.responses.api_error"), zap.String("error_message", response.Error.Message))
		return "", fmt.Errorf("%s", i18n.T("llm.responses.api_error_msg", response.Error.Message))
	}

	// 2. Verificar o status da resposta
	if response.Status == "incomplete" {
		if response.IncompleteDetails != nil && response.IncompleteDetails.Reason == "max_output_tokens" {
			c.logger.Warn(i18n.T("llm.responses.incomplete_max_tokens"), zap.Int("body_size", len(bodyBytes)))
			return "", errors.New(i18n.T("llm.responses.incomplete_max_tokens"))
		}
		return "", fmt.Errorf("%s", i18n.T("llm.responses.incomplete_unknown", response.Status))
	}

	// 3. Iterar para encontrar o texto apenas se o status for 'completed'
	var sb strings.Builder
	for _, item := range response.Output {
		// Procurar especificamente pelo item do tipo 'message'
		if item.Type == "message" {
			for _, content := range item.Content {
				// E dentro dele, pelo conteúdo do tipo 'output_text'
				if content.Type == "output_text" && strings.TrimSpace(content.Text) != "" {
					sb.WriteString(content.Text)
				}
			}
		}
	}

	if sb.Len() > 0 {
		return sb.String(), nil
	}

	// Se chegou aqui, não foi possível extrair
	c.logger.Warn(i18n.T("llm.warn.could_not_extract_text"), zap.Int("body_size", len(bodyBytes)))
	return "", errors.New(i18n.T("llm.warn.could_not_extract_text_error"))
}

// processStreamResponse handles SSE streaming responses from the ChatGPT backend.
func (c *OpenAIResponsesClient) processStreamResponse(resp *http.Response) (string, error) {
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, readErr := io.ReadAll(resp.Body)
		msg := "(unable to read response body)"
		if readErr == nil {
			msg = utils.SanitizeSensitiveText(string(bodyBytes))
		}
		return "", &utils.APIError{StatusCode: resp.StatusCode, Message: msg}
	}

	var sb strings.Builder
	scanner := bufio.NewScanner(resp.Body)
	// Increase buffer for potentially large SSE lines
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()

		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}

		var event struct {
			Type     string `json:"type"`
			Delta    string `json:"delta"`
			Response json.RawMessage `json:"response,omitempty"`
		}
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			continue
		}

		if event.Type == "response.output_text.delta" && event.Delta != "" {
			sb.WriteString(event.Delta)
		}

		// Extract usage from the response.completed event
		if event.Type == "response.completed" && event.Response != nil {
			var respData map[string]interface{}
			if err := json.Unmarshal(event.Response, &respData); err == nil {
				if usage := client.ParseOpenAIUsage(respData); usage != nil {
					c.usageState.StoreUsage(usage)
				}
				if status, ok := respData["status"].(string); ok && status != "" {
					c.usageState.StoreStopReason(status)
				}
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("%s: %w", i18n.T("llm.error.read_sse_stream"), err)
	}

	if sb.Len() == 0 {
		return "", errors.New(i18n.T("llm.error.no_text_from_sse"))
	}

	return sb.String(), nil
}

// buildOAuthPayload extracts system messages as instructions and builds
// structured conversation items for the ChatGPT backend Responses API.
func buildOAuthPayload(history []models.Message, prompt string) (string, []map[string]string) {
	var instructions strings.Builder
	var input []map[string]string

	for _, m := range history {
		role := strings.ToLower(strings.TrimSpace(m.Role))
		switch role {
		case "system":
			if instructions.Len() > 0 {
				instructions.WriteString("\n")
			}
			instructions.WriteString(m.Content)
		case "assistant":
			input = append(input, map[string]string{"role": "assistant", "content": m.Content})
		default:
			input = append(input, map[string]string{"role": "user", "content": m.Content})
		}
	}

	// Append current prompt if not already the last user message
	if len(history) == 0 || history[len(history)-1].Role != "user" || history[len(history)-1].Content != prompt {
		if strings.TrimSpace(prompt) != "" {
			input = append(input, map[string]string{"role": "user", "content": prompt})
		}
	}

	inst := instructions.String()
	if inst == "" {
		inst = "You are a helpful assistant."
	}

	return inst, input
}

// ListModels fetches available models. For OAuth tokens it uses the ChatGPT
// backend endpoint; for API keys it uses the standard OpenAI /v1/models.
func (c *OpenAIResponsesClient) ListModels(ctx context.Context) ([]client.ModelInfo, error) {
	isOAuth := strings.HasPrefix(c.apiKey, "oauth:")

	if isOAuth {
		models, err := c.listModelsOAuth(ctx)
		if err != nil {
			c.logger.Warn(i18n.T("llm.responses.oauth_listing_failed"),
				zap.Error(err))
			// Fallback: try standard OpenAI /v1/models with the OAuth token
			return c.listModelsAPIKey(ctx)
		}
		return models, nil
	}
	return c.listModelsAPIKey(ctx)
}

// listModelsOAuth fetches models from the ChatGPT backend (chatgpt.com).
func (c *OpenAIResponsesClient) listModelsOAuth(ctx context.Context) ([]client.ModelInfo, error) {
	// ChatGPT backend models endpoint
	baseURL := utils.GetEnvOrDefault("OPENAI_RESPONSES_API_URL", config.OpenAIOAuthResponsesURL)
	// Derive backend-api/models from the codex/responses URL
	modelsURL := strings.TrimSuffix(baseURL, "/codex/responses") + "/models"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, modelsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", i18n.T("llm.error.create_request_for", "OpenAI"), err)
	}
	req.Header.Set("Authorization", "Bearer "+auth.StripAuthPrefix(c.apiKey))
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", i18n.T("llm.responses.fetch_chatgpt_models_failed"), err)
	}
	defer func() { _ = resp.Body.Close() }()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", i18n.T("llm.error.read_response_for", "OpenAI"), err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s", i18n.T("llm.error.api_error_code", "ChatGPT", resp.StatusCode, utils.SanitizeSensitiveText(string(bodyBytes))))
	}

	// ChatGPT backend returns {"models": [{"slug": "gpt-4o", "title": "GPT-4o", ...}]}
	// or {"categories": [..., {"default_model": "...", "browsing_model": "..."}]}
	var result struct {
		Models []struct {
			Slug  string `json:"slug"`
			Title string `json:"title"`
		} `json:"models"`
		// Alternative format with categories
		Categories []struct {
			DefaultModel string `json:"default_model"`
			HumanName    string `json:"human_category_name"`
		} `json:"categories"`
	}
	if err := json.Unmarshal(bodyBytes, &result); err != nil {
		return nil, fmt.Errorf("%s: %w", i18n.T("llm.error.decode_response_for", "OpenAI"), err)
	}

	seen := make(map[string]bool)
	var modelList []client.ModelInfo

	// Parse models array
	for _, m := range result.Models {
		if m.Slug == "" || seen[m.Slug] {
			continue
		}
		seen[m.Slug] = true
		displayName := m.Title
		if displayName == "" {
			displayName = m.Slug
		}
		modelList = append(modelList, client.ModelInfo{
			ID:          m.Slug,
			DisplayName: displayName,
			Source:      client.ModelSourceAPI,
		})
		if _, ok := catalog.Resolve(catalog.ProviderOpenAI, m.Slug); !ok {
			catalog.Register(catalog.ModelMeta{
				ID:           m.Slug,
				Aliases:      []string{m.Slug},
				DisplayName:  displayName,
				Provider:     catalog.ProviderOpenAI,
				PreferredAPI: catalog.APIResponses,
			})
		}
	}

	// Parse categories (alternative format)
	for _, cat := range result.Categories {
		slug := cat.DefaultModel
		if slug == "" || seen[slug] {
			continue
		}
		seen[slug] = true
		modelList = append(modelList, client.ModelInfo{
			ID:          slug,
			DisplayName: slug,
			Source:      client.ModelSourceAPI,
		})
		if _, ok := catalog.Resolve(catalog.ProviderOpenAI, slug); !ok {
			catalog.Register(catalog.ModelMeta{
				ID:           slug,
				Aliases:      []string{slug},
				DisplayName:  slug,
				Provider:     catalog.ProviderOpenAI,
				PreferredAPI: catalog.APIResponses,
			})
		}
	}

	c.logger.Info(i18n.T("llm.responses.fetch_models_oauth"), zap.Int("count", len(modelList)))
	return modelList, nil
}

// listModelsAPIKey fetches models from the standard OpenAI /v1/models endpoint.
func (c *OpenAIResponsesClient) listModelsAPIKey(ctx context.Context) ([]client.ModelInfo, error) {
	modelsURL := utils.GetEnvOrDefault("OPENAI_API_URL", config.OpenAIAPIURL)
	modelsURL = strings.TrimSuffix(modelsURL, "/chat/completions") + "/models"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, modelsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", i18n.T("llm.error.create_request_for", "OpenAI"), err)
	}
	req.Header.Set("Authorization", "Bearer "+auth.StripAuthPrefix(c.apiKey))

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", i18n.T("llm.error.request_failed", "OpenAI"), err)
	}
	defer func() { _ = resp.Body.Close() }()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", i18n.T("llm.error.read_response_for", "OpenAI"), err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s", i18n.T("llm.error.api_error_code", "OpenAI", resp.StatusCode, utils.SanitizeSensitiveText(string(bodyBytes))))
	}

	var result struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(bodyBytes, &result); err != nil {
		return nil, fmt.Errorf("%s: %w", i18n.T("llm.error.decode_response_for", "OpenAI"), err)
	}

	var modelList []client.ModelInfo
	for _, m := range result.Data {
		id := strings.ToLower(m.ID)
		if !strings.HasPrefix(id, "gpt-") && !strings.HasPrefix(id, "o1-") &&
			!strings.HasPrefix(id, "o3-") && !strings.HasPrefix(id, "o4-") &&
			!strings.HasPrefix(id, "chatgpt-") {
			continue
		}
		modelList = append(modelList, client.ModelInfo{
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
				PreferredAPI: catalog.APIResponses,
			})
		}
	}

	c.logger.Info(i18n.T("llm.responses.fetch_models_apikey"), zap.Int("count", len(modelList)))
	return modelList, nil
}

func buildTextFromHistory(history []models.Message, prompt string) string {
	var b strings.Builder
	// opcional: preservar alguma instrução de sistema (quando houver)
	for _, m := range history {
		role := strings.ToLower(strings.TrimSpace(m.Role))
		switch role {
		case "system":
			b.WriteString("System: ")
		case "assistant":
			b.WriteString("Assistant: ")
		default:
			b.WriteString("User: ")
		}
		b.WriteString(m.Content)
		b.WriteString("\n")
	}
	b.WriteString("User: ")
	b.WriteString(prompt)
	return b.String()
}
