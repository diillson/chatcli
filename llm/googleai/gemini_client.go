/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
 */
package googleai

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

// GeminiClient implementa o cliente para interagir com a API do Google Gemini
type GeminiClient struct {
	apiKey      string
	model       string
	logger      *zap.Logger
	client      *http.Client
	maxAttempts int
	backoff     time.Duration
	baseURL     string
}

// NewGeminiClient cria uma nova instância de GeminiClient.
// Agora sem fallback interno: usa apenas os params passados (vindos do manager/ENVs).
func NewGeminiClient(apiKey, model string, logger *zap.Logger, maxAttempts int, backoff time.Duration) *GeminiClient {
	httpClient := utils.NewHTTPClient(logger, config.DefaultGoogleAITimeout)

	if model == "" {
		model = config.DefaultGoogleAIModel
	}

	// Log de inicialização
	logger.Info("Inicializando cliente Google AI (Gemini)",
		zap.String("model", model),
		zap.Int("max_attempts", maxAttempts),
		zap.Duration("backoff", backoff),
		zap.String("base_url", config.GoogleAIAPIURL),
		zap.Bool("api_key_configured", apiKey != ""))

	return &GeminiClient{
		apiKey:      apiKey,
		model:       model,
		logger:      logger,
		client:      httpClient,
		maxAttempts: maxAttempts,
		backoff:     backoff,
		baseURL:     config.GoogleAIAPIURL,
	}
}

// GetModelName retorna o nome amigável do modelo Gemini
func (c *GeminiClient) GetModelName() string {
	return catalog.GetDisplayName(catalog.ProviderGoogleAI, c.model)
}

// SendPrompt envia um prompt para o Gemini e retorna a resposta
func (c *GeminiClient) SendPrompt(ctx context.Context, prompt string, history []models.Message, maxTokens int) (string, error) {
	effectiveMaxTokens := maxTokens
	if effectiveMaxTokens <= 0 {
		effectiveMaxTokens = c.getMaxTokens() // Fallback para a lógica antiga se nada for passado
	}

	// ... (lógica de build do payload, logs, etc., permanece a mesma)
	c.logger.Info("Iniciando requisição para Google AI",
		zap.String("model", c.model),
		zap.Int("history_length", len(history)),
		zap.Int("prompt_length", len(prompt)))

	contents, systemInstruction := c.buildContentsAndSystem(history, prompt)

	if len(contents) == 0 && strings.TrimSpace(prompt) != "" {
		contents = []map[string]interface{}{
			{
				"role": "user",
				"parts": []map[string]string{
					{"text": prompt},
				},
			},
		}
	}

	generationConfig := map[string]interface{}{
		"temperature":     0.7,
		"topP":            0.95,
		"topK":            40,
		"maxOutputTokens": effectiveMaxTokens,
	}

	reqBody := map[string]interface{}{
		"contents":         contents,
		"generationConfig": generationConfig,
		"safetySettings":   c.getSafetySettings(),
	}
	if systemInstruction != nil {
		reqBody["system_instruction"] = systemInstruction
	}

	jsonValue, err := json.Marshal(reqBody)
	if err != nil {
		c.logger.Error("Erro ao marshalizar o payload para Google AI", zap.Error(err), zap.String("model", c.model))
		return "", fmt.Errorf("erro ao preparar a requisição: %w", err)
	}

	c.logger.Debug("Payload preparado", zap.Int("payload_size", len(jsonValue)), zap.String("model", c.model))

	// Agora use Retry para encapsular a lógica de requisição e parsing
	response, err := utils.Retry(ctx, c.logger, c.maxAttempts, c.backoff, func(ctx context.Context) (string, error) {
		response, err := c.executeRequest(ctx, jsonValue)
		if err != nil {
			return "", err
		}
		return response, nil
	})

	if err != nil {
		c.logger.Error("Erro ao obter resposta do Google AI após retries", zap.Error(err))
		return "", err
	}

	c.logger.Info("Resposta recebida do Google AI com sucesso",
		zap.Int("response_length", len(response)))
	return response, nil
}

func (c *GeminiClient) buildContentsAndSystem(history []models.Message, prompt string) ([]map[string]interface{}, map[string]interface{}) {
	var contents []map[string]interface{}
	var systemParts []map[string]string

	// Monta o turn-by-turn do histórico
	for _, msg := range history {
		switch strings.ToLower(msg.Role) {
		case "assistant":
			contents = append(contents, map[string]interface{}{
				"role": "model",
				"parts": []map[string]string{
					{"text": msg.Content},
				},
			})
		case "system":
			// Gemini v1beta aceita system_instruction como top-level.
			systemParts = append(systemParts, map[string]string{"text": msg.Content})
		default: // "user"
			contents = append(contents, map[string]interface{}{
				"role": "user",
				"parts": []map[string]string{
					{"text": msg.Content},
				},
			})
		}
	}

	var systemInstruction map[string]interface{}
	if len(systemParts) > 0 {
		systemInstruction = map[string]interface{}{
			"parts": systemParts,
		}
	}

	return contents, systemInstruction
}

// executeRequest executa a requisição HTTP para a API do Gemini
func (c *GeminiClient) executeRequest(ctx context.Context, jsonValue []byte) (string, error) {
	url := fmt.Sprintf("%s/models/%s:generateContent?key=%s", c.baseURL, c.model, c.apiKey)
	safeURL := fmt.Sprintf("%s/models/%s:generateContent?key=[REDACTED]", c.baseURL, c.model)

	c.logger.Debug("Enviando requisição POST para Google AI",
		zap.String("url", safeURL),
		zap.String("model", c.model))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(string(jsonValue)))
	if err != nil {
		// Este erro não contém a URL, então é seguro logar diretamente.
		c.logger.Error("Erro ao criar requisição HTTP para Google AI",
			zap.Error(err),
			zap.String("model", c.model))
		return "", fmt.Errorf("erro ao criar requisição: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	startTime := time.Now()
	resp, err := c.client.Do(req)
	duration := time.Since(startTime)

	if err != nil {
		sanitizedErr := fmt.Errorf("erro na requisição para %s: %w", safeURL, err)

		// Usamos o erro sanitizado para o log e para o retorno.
		c.logger.Error("Erro na requisição HTTP para Google AI",
			zap.Error(sanitizedErr), // LOG SEGURO
			zap.Duration("duration", duration),
			zap.String("model", c.model))

		return "", sanitizedErr // RETORNO SEGURO
	}
	defer resp.Body.Close()

	c.logger.Debug("Resposta HTTP recebida",
		zap.Int("status_code", resp.StatusCode),
		zap.Duration("duration", duration),
		zap.String("model", c.model))

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		c.logger.Error("Erro ao ler corpo da resposta do Google AI",
			zap.Error(err),
			zap.String("model", c.model))
		return "", fmt.Errorf("erro ao ler resposta: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		// Retorna diretamente APIError
		return "", &utils.APIError{StatusCode: resp.StatusCode, Message: string(bodyBytes)}
	}

	return c.parseResponse(bodyBytes)
}

// parseResponse extrai o texto da resposta do Gemini
func (c *GeminiClient) parseResponse(bodyBytes []byte) (string, error) {
	c.logger.Debug("Iniciando parse da resposta do Google AI",
		zap.Int("body_size", len(bodyBytes)),
		zap.String("model", c.model))

	var result struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
			FinishReason  string `json:"finishReason"`
			SafetyRatings []struct {
				Category    string `json:"category"`
				Probability string `json:"probability"`
			} `json:"safetyRatings"`
		} `json:"candidates"`
		UsageMetadata struct {
			PromptTokenCount     int `json:"promptTokenCount"`
			CandidatesTokenCount int `json:"candidatesTokenCount"`
			TotalTokenCount      int `json:"totalTokenCount"`
		} `json:"usageMetadata"`
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Status  string `json:"status"`
		} `json:"error,omitempty"`
	}

	if err := json.Unmarshal(bodyBytes, &result); err != nil {
		sanitizedResponse := c.sanitizeErrorResponse(string(bodyBytes))

		c.logger.Error("Erro ao decodificar resposta JSON do Google AI",
			zap.Error(err),
			zap.String("model", c.model),
			zap.String("raw_response", sanitizedResponse[:min(500, len(sanitizedResponse))]))
		return "", fmt.Errorf("erro ao decodificar resposta: %w", err)
	}

	// Verificar se há erro na resposta
	if result.Error.Code != 0 {
		c.logger.Error("Erro retornado pela API do Google AI",
			zap.Int("error_code", result.Error.Code),
			zap.String("error_message", result.Error.Message),
			zap.String("error_status", result.Error.Status),
			zap.String("model", c.model))
		return "", fmt.Errorf("erro da API: %s (código: %d)", result.Error.Message, result.Error.Code)
	}

	if len(result.Candidates) == 0 {
		c.logger.Error("Nenhum candidato na resposta do Google AI",
			zap.String("model", c.model))
		return "", fmt.Errorf("nenhuma resposta recebida do Google AI")
	}

	// Log detalhado de uso de tokens
	c.logger.Info("Estatísticas de uso de tokens do Google AI",
		zap.Int("prompt_tokens", result.UsageMetadata.PromptTokenCount),
		zap.Int("response_tokens", result.UsageMetadata.CandidatesTokenCount),
		zap.Int("total_tokens", result.UsageMetadata.TotalTokenCount),
		zap.String("model", c.model))

	// Log do finish reason
	if len(result.Candidates) > 0 {
		c.logger.Debug("Razão de finalização da resposta",
			zap.String("finish_reason", result.Candidates[0].FinishReason),
			zap.String("model", c.model))

		// Log de safety ratings se disponível
		if len(result.Candidates[0].SafetyRatings) > 0 {
			for _, rating := range result.Candidates[0].SafetyRatings {
				c.logger.Debug("Safety rating",
					zap.String("category", rating.Category),
					zap.String("probability", rating.Probability),
					zap.String("model", c.model))
			}
		}
	}

	var responseText strings.Builder
	partsCount := 0
	for _, part := range result.Candidates[0].Content.Parts {
		responseText.WriteString(part.Text)
		partsCount++
	}

	c.logger.Debug("Resposta processada",
		zap.Int("parts_count", partsCount),
		zap.Int("total_length", responseText.Len()),
		zap.String("model", c.model))

	if responseText.Len() == 0 {
		c.logger.Error("Resposta vazia do Google AI",
			zap.String("model", c.model))
		return "", fmt.Errorf("resposta vazia do Google AI")
	}

	c.logger.Info("Parse da resposta do Google AI concluído com sucesso",
		zap.Int("response_length", responseText.Len()),
		zap.String("model", c.model))

	return responseText.String(), nil
}

// sanitizeErrorResponse remove informações sensíveis de mensagens de erro
func (c *GeminiClient) sanitizeErrorResponse(response string) string {
	// Remover padrões de API key
	patterns := []struct {
		pattern     *regexp.Regexp
		replacement string
	}{
		{regexp.MustCompile(`key=[\w-]+`), "key=[REDACTED]"},
		{regexp.MustCompile(`API key.*?: [\w-]+`), "API key: [REDACTED]"},
		{regexp.MustCompile(`"api_key":\s*"[^"]+"`), `"api_key":"[REDACTED]"`},
		{regexp.MustCompile(`AIza[\w-]{35}`), "[REDACTED_API_KEY]"}, // Padrão de API key do Google
	}

	sanitized := response
	for _, p := range patterns {
		sanitized = p.pattern.ReplaceAllString(sanitized, p.replacement)
	}

	return sanitized
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// getMaxTokens obtém o limite de tokens configurado
func (c *GeminiClient) getMaxTokens() int {
	if tokenStr := os.Getenv("GOOGLEAI_MAX_TOKENS"); tokenStr != "" {
		if parsedTokens, err := strconv.Atoi(tokenStr); err == nil && parsedTokens > 0 {
			c.logger.Debug("Usando max_tokens personalizado", zap.Int("max_tokens", parsedTokens))
			return parsedTokens
		}
	}

	// Usar o catálogo para obter limite baseado no modelo
	return catalog.GetMaxTokens(catalog.ProviderGoogleAI, c.model, 0)
}

// getSafetySettings retorna as configurações de segurança para o Gemini
func (c *GeminiClient) getSafetySettings() []map[string]string {
	// Configurações mais permissivas para uso em desenvolvimento
	return []map[string]string{
		{
			"category":  "HARM_CATEGORY_HARASSMENT",
			"threshold": "BLOCK_ONLY_HIGH",
		},
		{
			"category":  "HARM_CATEGORY_HATE_SPEECH",
			"threshold": "BLOCK_ONLY_HIGH",
		},
		{
			"category":  "HARM_CATEGORY_SEXUALLY_EXPLICIT",
			"threshold": "BLOCK_ONLY_HIGH",
		},
		{
			"category":  "HARM_CATEGORY_DANGEROUS_CONTENT",
			"threshold": "BLOCK_ONLY_HIGH",
		},
	}
}
