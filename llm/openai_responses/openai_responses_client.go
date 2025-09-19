/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
 */
package openai_responses

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

// OpenAIResponsesClient implementa o cliente para a API /v1/responses
type OpenAIResponsesClient struct {
	apiKey      string
	model       string
	logger      *zap.Logger
	httpClient  *http.Client
	maxAttempts int
	backoff     time.Duration
}

func NewOpenAIResponsesClient(apiKey, model string, logger *zap.Logger, maxAttempts int, backoff time.Duration) *OpenAIResponsesClient {
	httpClient := utils.NewHTTPClient(logger, 900*time.Second)
	if maxAttempts <= 0 {
		maxAttempts = config.OpenAIDefaultMaxAttempts
	}
	if backoff <= 0 {
		backoff = config.OpenAIDefaultBackoff
	}
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
		effectiveMaxTokens = c.getMaxTokens() // valor padrão
	}
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

	reqBody := map[string]interface{}{
		"model":             c.model,
		"input":             input,
		"max_output_tokens": effectiveMaxTokens,
	}

	jsonValue, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("erro ao preparar payload para Responses API: %w", err)
	}

	var backoff = c.backoff
	for attempt := 1; attempt <= c.maxAttempts; attempt++ {
		resp, err := c.sendRequest(ctx, jsonValue)
		if err != nil {
			if utils.IsTemporaryError(err) {
				c.logger.Warn("Erro temporário ao chamar OpenAI Responses",
					zap.Int("attempt", attempt),
					zap.Error(err),
					zap.Duration("backoff", backoff),
				)
				if attempt < c.maxAttempts {
					time.Sleep(backoff)
					backoff *= 2
					continue
				}
			}
			return "", fmt.Errorf("erro na requisição à OpenAI Responses: %w", err)
		}

		out, err := c.processResponse(resp)
		if err != nil {
			return "", err
		}
		return out, nil
	}

	return "", fmt.Errorf("falha ao obter resposta da OpenAI Responses após %d tentativas", c.maxAttempts)
}

func (c *OpenAIResponsesClient) sendRequest(ctx context.Context, body []byte) (*http.Response, error) {
	// Usar a variável de ambiente se estiver definida, senão usar a constante
	apiURL := utils.GetEnvOrDefault("OPENAI_RESPONSES_API_URL", config.OpenAIResponsesAPIURL)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, utils.NewJSONReader(body))
	if err != nil {
		return nil, fmt.Errorf("erro ao criar requisição para Responses API: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

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

	// Adicionar log de depuração para sempre vermos o corpo da resposta
	c.logger.Debug("Corpo da resposta recebido da OpenAI Responses (RAW)",
		zap.ByteString("body", bodyBytes))

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		errMsg := fmt.Sprintf("erro na Responses API: status %d, resposta: %s", resp.StatusCode, string(bodyBytes))
		c.logger.Error("Resposta de erro da OpenAI Responses",
			zap.Int("status", resp.StatusCode),
			zap.String("resposta", string(bodyBytes)),
		)
		return "", errors.New(errMsg)
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

	// 2. Verificar o status da resposta - esta é a nova lógica crucial
	if response.Status == "incomplete" {
		if response.IncompleteDetails != nil && response.IncompleteDetails.Reason == "max_output_tokens" {
			c.logger.Warn("Resposta incompleta devido a max_output_tokens baixo.", zap.ByteString("body", bodyBytes))
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
	c.logger.Warn("Não foi possível extrair texto da resposta, mesmo com status 'completed'.", zap.ByteString("body", bodyBytes))
	return "", fmt.Errorf("não foi possível extrair o texto da resposta da Responses API")
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
