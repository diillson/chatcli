/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
 */
package stackspotai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/diillson/chatcli/config"
	"github.com/diillson/chatcli/llm/token"

	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/utils"
	"go.uber.org/zap"
)

// StackSpotClient implementa o cliente para interagir com a API da StackSpot
type StackSpotClient struct {
	tokenManager    token.Manager
	slug            string
	logger          *zap.Logger
	client          *http.Client
	maxAttempts     int
	backoff         time.Duration
	baseURL         string
	responseTimeout time.Duration
}

// NewStackSpotClient cria uma nova instância de StackSpotClient.
func NewStackSpotClient(tokenManager token.Manager, slug string, logger *zap.Logger, maxAttempts int, backoff time.Duration) *StackSpotClient {
	httpClient := utils.NewHTTPClient(logger, 900*time.Second)
	if maxAttempts <= 0 {
		maxAttempts = config.DefaultMaxAttempts
	}
	if backoff <= 0 {
		backoff = config.DefaultBackoff
	}

	return &StackSpotClient{
		tokenManager:    tokenManager,
		slug:            slug,
		logger:          logger,
		client:          httpClient,
		maxAttempts:     maxAttempts,
		backoff:         backoff,
		baseURL:         config.StackSpotBaseURL,
		responseTimeout: config.StackSpotResponseTimeout,
	}
}

// GetModelName retorna o nome do modelo de linguagem utilizado pelo cliente.
func (c *StackSpotClient) GetModelName() string {
	return config.StackSpotDefaultModel
}

// SendPrompt envia um prompt para o modelo de linguagem e retorna a resposta.
func (c *StackSpotClient) SendPrompt(ctx context.Context, prompt string, history []models.Message, maxTokens int) (string, error) {
	// Formatar o histórico da conversa
	conversationHistory := formatConversationHistory(history)

	// Concatenar o histórico com o prompt atual
	fullPrompt := fmt.Sprintf("%sUsuário: %s", conversationHistory, prompt)

	// Enviar o prompt completo e obter o responseID
	responseID, err := c.sendRequestToLLMWithRetry(ctx, fullPrompt)
	if err != nil {
		c.logger.Error("Erro ao enviar a requisição para a LLM", zap.Error(err))
		return "", fmt.Errorf("erro ao enviar a requisição: %w", err)
	}

	// Obter a resposta da LLM
	llmResponse, err := c.pollLLMResponse(ctx, responseID)
	if err != nil {
		c.logger.Error("Erro ao obter a resposta da LLM", zap.Error(err))
		return "", err
	}

	return llmResponse, nil
}

// formatConversationHistory formata o histórico da conversa para ser enviado à LLM
func formatConversationHistory(history []models.Message) string {
	var conversationBuilder strings.Builder
	for _, msg := range history {
		role := "Usuário"
		if msg.Role == "assistant" {
			role = "Assistente"
		}
		conversationBuilder.WriteString(fmt.Sprintf("%s: %s\n", role, msg.Content))
	}
	return conversationBuilder.String()
}

// sendRequestToLLMWithRetry envia a requisição para a LLM com tentativas de retry
func (c *StackSpotClient) sendRequestToLLMWithRetry(ctx context.Context, prompt string) (string, error) {
	var backoff = c.backoff

	for attempt := 1; attempt <= c.maxAttempts; attempt++ {
		responseID, err := c.executeWithTokenRetry(ctx, func(token string) (string, error) {
			return c.sendRequestToLLM(ctx, prompt, token)
		})

		if err != nil {
			if utils.IsTemporaryError(err) {
				c.logger.Warn("Erro temporário ao enviar requisição para StackSpotAI",
					zap.Int("attempt", attempt),
					zap.Error(err),
					zap.Duration("backoff", backoff),
				)
				if attempt < c.maxAttempts {
					time.Sleep(backoff)
					backoff *= 2 // Backoff exponencial
					continue
				}
			}
			return "", fmt.Errorf("erro ao enviar requisição para StackSpotAI: %w", err)
		}

		return responseID, nil
	}

	return "", fmt.Errorf("falha ao enviar requisição para StackSpotAI após %d tentativas", c.maxAttempts)
}

// sendRequestToLLM envia o prompt para a LLM e retorna o responseID
func (c *StackSpotClient) sendRequestToLLM(ctx context.Context, prompt, accessToken string) (string, error) {
	conversationID := utils.GenerateUUID()

	slug, _ := c.tokenManager.GetSlugAndTenantName()

	url := fmt.Sprintf("%s/create-execution/%s?conversation_id=%s", c.baseURL, slug, conversationID)
	c.logger.Info("Enviando requisição para URL", zap.String("url", url))

	requestBody := map[string]string{
		"input_data": prompt,
	}
	jsonValue, err := json.Marshal(requestBody)
	if err != nil {
		c.logger.Error("Erro ao marshalizar o corpo da requisição", zap.Error(err))
		return "", fmt.Errorf("erro ao preparar a requisição: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(string(jsonValue)))
	if err != nil {
		c.logger.Error("Erro ao criar a requisição POST", zap.Error(err))
		return "", fmt.Errorf("erro ao criar a requisição: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := c.client.Do(req)
	if err != nil {
		c.logger.Error("Erro ao fazer a requisição POST", zap.Error(err))
		return "", fmt.Errorf("erro ao fazer a requisição: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		c.logger.Error("Erro ao ler a resposta POST", zap.Error(err))
		return "", fmt.Errorf("erro ao ler a resposta: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		c.logger.Error("Erro na requisição à LLM",
			zap.Int("status_code", resp.StatusCode),
			zap.String("response", string(bodyBytes)),
		)
		return "", fmt.Errorf("erro na requisição à LLM: status %d, resposta: %s", resp.StatusCode, string(bodyBytes))
	}

	var responseID string
	if err := json.Unmarshal(bodyBytes, &responseID); err != nil {
		c.logger.Error("Erro ao deserializar o responseID", zap.Error(err))
		return "", fmt.Errorf("erro ao deserializar o responseID: %w", err)
	}

	c.logger.Info("Response ID recebido", zap.String("response_id", responseID))
	return responseID, nil
}

// executeWithTokenRetry executa uma função que faz uma requisição HTTP, garantindo que o token seja renovado se necessário.
// Ele tenta a requisição novamente se o token expirar (erro 403).
func (c *StackSpotClient) executeWithTokenRetry(ctx context.Context, requestFunc func(string) (string, error)) (string, error) {
	// Obtém o token de acesso válido
	token, err := c.tokenManager.GetAccessToken(ctx)
	if err != nil {
		c.logger.Error("Erro ao obter o token", zap.Error(err))
		return "", fmt.Errorf("erro ao obter o token: %w", err)
	}

	// Tenta executar a função de requisição com o token atual
	response, err := requestFunc(token)
	if err != nil {
		// Se o erro for 403, tenta renovar o token e refazer a requisição
		if strings.Contains(err.Error(), "403") || strings.Contains(err.Error(), "401") {
			c.logger.Warn("Token expirado ou inválido, tentando renovar...")

			// Tenta renovar o token
			newToken, tokenErr := c.tokenManager.RefreshToken(ctx)
			if tokenErr != nil {
				c.logger.Error("Erro ao renovar o token", zap.Error(tokenErr))
				return "", fmt.Errorf("erro ao renovar o token: %w", tokenErr)
			}

			c.logger.Info("Token renovado com sucesso, tentando novamente...")

			// Tenta a requisição novamente com o novo token
			return requestFunc(newToken)
		}

		// Se não for um erro 403, retorna o erro original
		return "", err
	}

	return response, nil
}

// pollLLMResponse faz polling para obter a resposta da LLM
func (c *StackSpotClient) pollLLMResponse(ctx context.Context, responseID string) (string, error) {
	ticker := time.NewTicker(c.responseTimeout)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			c.logger.Warn("Contexto cancelado ou expirado", zap.Error(ctx.Err()))
			return "", fmt.Errorf("contexto cancelado ou expirado: %w", ctx.Err())
		case <-ticker.C:
			llmResponse, err := c.executeWithTokenRetry(ctx, func(token string) (string, error) {
				return c.getLLMResponse(ctx, responseID, token)
			})

			if err != nil {
				if strings.Contains(err.Error(), "resposta ainda não está pronta") {
					c.logger.Info("Resposta ainda não está pronta, tentando novamente...")
					continue
				}
				return "", err
			}

			return llmResponse, nil
		}
	}
}

// getLLMResponse obtém a resposta da LLM usando o responseID
func (c *StackSpotClient) getLLMResponse(ctx context.Context, responseID, accessToken string) (string, error) {
	url := fmt.Sprintf("%s/callback/%s", c.baseURL, responseID)
	c.logger.Info("Fazendo GET para URL", zap.String("url", url))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		c.logger.Error("Erro ao criar a requisição GET", zap.Error(err))
		return "", fmt.Errorf("erro ao criar a requisição GET: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		c.logger.Error("Erro na requisição GET para a LLM", zap.Error(err))
		return "", fmt.Errorf("erro na requisição GET para a LLM: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		c.logger.Error("Erro ao ler o corpo da resposta da LLM", zap.Error(err))
		return "", fmt.Errorf("erro ao ler o corpo da resposta da LLM: %w", err)
	}

	c.logger.Info("Resposta recebida", zap.Int("status_code", resp.StatusCode), zap.String("response", string(bodyBytes)))

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("erro na requisição de callback: status %d, resposta: %s", resp.StatusCode, string(bodyBytes))
	}

	var callbackResponse CallbackResponse
	if err := json.Unmarshal(bodyBytes, &callbackResponse); err != nil {
		c.logger.Error("Erro ao deserializar a resposta JSON", zap.Error(err))
		return "", fmt.Errorf("erro ao deserializar a resposta JSON: %w", err)
	}

	switch callbackResponse.Progress.Status {
	case "COMPLETED":
		if len(callbackResponse.Steps) > 0 {
			lastStep := callbackResponse.Steps[len(callbackResponse.Steps)-1]
			llmAnswer := lastStep.StepResult.Answer
			return llmAnswer, nil
		}
		c.logger.Error("Nenhuma resposta disponível na execução COMPLETED", zap.Any("CallbackResponse", callbackResponse))
		return "", fmt.Errorf("nenhuma resposta disponível")
	case "FAILURE":
		c.logger.Error("A execução falhou", zap.String("status", callbackResponse.Progress.Status))
		return "", fmt.Errorf("a execução da LLM falhou")
	default:
		c.logger.Info("Status da execução", zap.String("status", callbackResponse.Progress.Status))
		return "", fmt.Errorf("resposta ainda não está pronta")
	}
}

// Estruturas para decodificar a resposta da LLM

type CallbackResponse struct {
	ExecutionID      string   `json:"execution_id"`
	QuickCommandSlug string   `json:"quick_command_slug"`
	ConversationID   string   `json:"conversation_id"`
	Progress         Progress `json:"progress"`
	Steps            []Step   `json:"steps"`
	Result           string   `json:"result"`
}

type Progress struct {
	Start               string  `json:"start"`
	End                 string  `json:"end"`
	Duration            int     `json:"duration"`
	ExecutionPercentage float64 `json:"execution_percentage"`
	Status              string  `json:"status"`
}

type Step struct {
	StepName       string     `json:"step_name"`
	ExecutionOrder int        `json:"execution_order"`
	Type           string     `json:"type"`
	StepResult     StepResult `json:"step_result"`
}

type Source struct {
	Type          string  `json:"type,omitempty"`
	Name          string  `json:"name,omitempty"`
	Slug          string  `json:"slug,omitempty"`
	DocumentType  string  `json:"document_type,omitempty"`
	DocumentScore float64 `json:"document_score,omitempty"`
	DocumentID    string  `json:"document_id,omitempty"`
}

type StepResult struct {
	Answer  string   `json:"answer"`
	Sources []Source `json:"sources"`
}
