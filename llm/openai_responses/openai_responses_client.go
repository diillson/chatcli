/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
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
	"github.com/diillson/chatcli/llm/catalog"
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
}

// NewOpenAIResponsesClient cria uma nova instância de OpenAIResponsesClient.
// Agora sem fallback interno: usa apenas os params passados (vindos do manager/ENVs).
func NewOpenAIResponsesClient(apiKey, model string, logger *zap.Logger, maxAttempts int, backoff time.Duration) *OpenAIResponsesClient {
	httpClient := utils.NewHTTPClient(logger, 900*time.Second)
	return &OpenAIResponsesClient{
		apiKey:      apiKey,
		model:       model,
		logger:      logger,
		httpClient:  httpClient,
		maxAttempts: maxAttempts,
		backoff:     backoff,
	}
}

func (c *OpenAIResponsesClient) GetModelName() string {
	return c.model
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
		return "", fmt.Errorf("erro ao preparar payload para Responses API: %w", err)
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
		c.logger.Error("Erro ao obter resposta da OpenAI Responses após retries", zap.Error(err))
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
		return nil, fmt.Errorf("erro ao criar requisição para Responses API: %w", err)
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
		return "", fmt.Errorf("erro ao ler resposta da Responses API: %w", err)
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
		return "", fmt.Errorf("erro ao decodificar resposta da Responses API: %w", err)
	}

	// Tentar extrair do caminho simples primeiro (comum em mocks e respostas diretas)
	if response.OutputText != "" {
		return response.OutputText, nil
	}

	// 1. Verificar se a API retornou um erro explícito no corpo
	if response.Error != nil && response.Error.Message != "" {
		c.logger.Error("API da OpenAI Responses retornou um erro no payload", zap.String("error_message", response.Error.Message))
		return "", fmt.Errorf("erro da API OpenAI: %s", response.Error.Message)
	}

	// 2. Verificar o status da resposta
	if response.Status == "incomplete" {
		if response.IncompleteDetails != nil && response.IncompleteDetails.Reason == "max_output_tokens" {
			c.logger.Warn("Resposta incompleta devido a max_output_tokens baixo.", zap.Int("body_size", len(bodyBytes)))
			return "", errors.New("a resposta da OpenAI foi incompleta, o valor de 'max_output_tokens' é muito baixo")
		}
		return "", fmt.Errorf("a resposta da OpenAI foi incompleta por um motivo desconhecido (status: %s)", response.Status)
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
	c.logger.Warn("Não foi possível extrair texto da resposta, mesmo com status 'completed'.", zap.Int("body_size", len(bodyBytes)))
	return "", fmt.Errorf("não foi possível extrair o texto da resposta da Responses API")
}

// processStreamResponse handles SSE streaming responses from the ChatGPT backend.
func (c *OpenAIResponsesClient) processStreamResponse(resp *http.Response) (string, error) {
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return "", &utils.APIError{StatusCode: resp.StatusCode, Message: utils.SanitizeSensitiveText(string(bodyBytes))}
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
			Type  string `json:"type"`
			Delta string `json:"delta"`
		}
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			continue
		}

		if event.Type == "response.output_text.delta" && event.Delta != "" {
			sb.WriteString(event.Delta)
		}
	}

	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("erro ao ler stream SSE: %w", err)
	}

	if sb.Len() == 0 {
		return "", fmt.Errorf("nenhum texto extraído do stream SSE da Responses API")
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
