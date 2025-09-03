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
	"strings"
	"time"

	"github.com/diillson/chatcli/config"
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

func (c *OpenAIResponsesClient) SendPrompt(ctx context.Context, prompt string, history []models.Message) (string, error) {
	// Flatten do histórico para um texto coerente, sem duplicar o prompt.
	// Aqui construímos SOMENTE a partir do history:
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
		"model": c.model,
		"input": input,
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

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		errMsg := fmt.Sprintf("erro na Responses API: status %d, resposta: %s", resp.StatusCode, string(bodyBytes))
		c.logger.Error("Resposta de erro da OpenAI Responses",
			zap.Int("status", resp.StatusCode),
			zap.String("resposta", string(bodyBytes)),
		)
		return "", errors.New(errMsg)
	}

	// Estrutura genérica para várias formas de retorno da Responses API
	var generic struct {
		// caminho direto mais comum
		OutputText string `json:"output_text"`
		// caminho rich
		Output []struct {
			Type    string `json:"type"` // "message"
			Role    string `json:"role"` // "assistant"
			Content []struct {
				Type string `json:"type"` // "output_text", "input_text", etc
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
		Content any `json:"content"`
	}

	if err := json.Unmarshal(bodyBytes, &generic); err != nil {
		return "", fmt.Errorf("erro ao decodificar resposta da Responses API: %w", err)
	}

	// 1) Se existir output_text, é o mais direto
	if strings.TrimSpace(generic.OutputText) != "" {
		return generic.OutputText, nil
	}

	// 2) Varredura do array "output"
	var sb strings.Builder
	for _, item := range generic.Output {
		for _, c := range item.Content {
			if strings.TrimSpace(c.Text) != "" {
				sb.WriteString(c.Text)
			}
		}
	}
	if sb.Len() > 0 {
		return sb.String(), nil
	}

	// 3) fallback: tentar extrair texto credível de "content" genérico se vier em outro formato
	if generic.Content != nil {
		if txt := tryExtractText(generic.Content); txt != "" {
			return txt, nil
		}
	}

	// Se chegou aqui, não foi possível extrair
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

// tryExtractText tenta extrair texto de uma estrutura arbitrária (best-effort)
func tryExtractText(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case []any:
		var sb strings.Builder
		for _, it := range t {
			if s := tryExtractText(it); s != "" {
				sb.WriteString(s)
			}
		}
		return sb.String()
	case map[string]any:
		// procurar chaves prováveis
		if s, ok := t["text"].(string); ok && strings.TrimSpace(s) != "" {
			return s
		}
		if s, ok := t["output_text"].(string); ok && strings.TrimSpace(s) != "" {
			return s
		}
		// procurar nested
		for _, val := range t {
			if s := tryExtractText(val); s != "" {
				return s
			}
		}
		return ""
	default:
		return ""
	}
}
