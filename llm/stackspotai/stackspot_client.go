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
	conversationHistory := formatConversationHistory(history)
	fullPrompt := fmt.Sprintf("%sUsuário: %s", conversationHistory, prompt)

	llmResponse, err := utils.Retry(ctx, c.logger, c.maxAttempts, c.backoff, func(ctx context.Context) (string, error) {
		responseID, err := c.executeWithTokenRetry(ctx, func(token string) (string, error) {
			return c.sendRequestToLLM(ctx, fullPrompt, token)
		})
		if err != nil {
			return "", err
		}
		return c.pollLLMResponse(ctx, responseID)
	})

	if err != nil {
		c.logger.Error("Erro ao obter a resposta da LLM após retries", zap.Error(err))
		return "", err
	}

	return llmResponse, nil
}

func (c *StackSpotClient) sendRequestToLLM(ctx context.Context, prompt, accessToken string) (string, error) {
	conversationID := utils.GenerateUUID()
	slug, _ := c.tokenManager.GetSlugAndTenantName()
	url := fmt.Sprintf("%s/create-execution/%s?conversation_id=%s", c.baseURL, slug, conversationID)

	requestBody := map[string]string{"input_data": prompt}
	jsonValue, err := json.Marshal(requestBody)
	if err != nil {
		return "", fmt.Errorf("erro ao preparar a requisição: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(string(jsonValue)))
	if err != nil {
		return "", fmt.Errorf("erro ao criar a requisição: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := c.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("erro ao fazer a requisição: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("erro ao ler a resposta: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", &utils.APIError{StatusCode: resp.StatusCode, Message: string(bodyBytes)}
	}

	var responseID string
	if err := json.Unmarshal(bodyBytes, &responseID); err != nil {
		return "", fmt.Errorf("erro ao deserializar o responseID: %w", err)
	}

	c.logger.Info("Response ID recebido", zap.String("response_id", responseID))
	return responseID, nil
}

func (c *StackSpotClient) pollLLMResponse(ctx context.Context, responseID string) (string, error) {
	ticker := time.NewTicker(c.responseTimeout)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("contexto cancelado ou expirado: %w", ctx.Err())
		case <-ticker.C:
			llmResponse, err := c.executeWithTokenRetry(ctx, func(token string) (string, error) {
				return c.getLLMResponse(ctx, responseID, token)
			})

			if err != nil {
				if strings.Contains(err.Error(), "resposta ainda não está pronta") {
					continue
				}
				return "", err
			}
			return llmResponse, nil
		}
	}
}

func (c *StackSpotClient) getLLMResponse(ctx context.Context, responseID, accessToken string) (string, error) {
	url := fmt.Sprintf("%s/callback/%s", c.baseURL, responseID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("erro ao criar a requisição GET: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("erro na requisição GET para a LLM: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("erro ao ler o corpo da resposta da LLM: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", &utils.APIError{StatusCode: resp.StatusCode, Message: string(bodyBytes)}
	}

	var callbackResponse CallbackResponse
	if err := json.Unmarshal(bodyBytes, &callbackResponse); err != nil {
		return "", fmt.Errorf("erro ao deserializar a resposta JSON: %w", err)
	}

	switch callbackResponse.Progress.Status {
	case "COMPLETED":
		if len(callbackResponse.Steps) > 0 {
			return callbackResponse.Steps[len(callbackResponse.Steps)-1].StepResult.Answer, nil
		}
		return "", fmt.Errorf("nenhuma resposta disponível")
	case "FAILURE":
		return "", fmt.Errorf("a execução da LLM falhou")
	default:
		return "", fmt.Errorf("resposta ainda não está pronta")
	}
}

func (c *StackSpotClient) executeWithTokenRetry(ctx context.Context, requestFunc func(string) (string, error)) (string, error) {
	token, err := c.tokenManager.GetAccessToken(ctx)
	if err != nil {
		return "", fmt.Errorf("erro ao obter o token: %w", err)
	}

	response, err := requestFunc(token)
	if err != nil {
		if strings.Contains(err.Error(), "403") || strings.Contains(err.Error(), "401") {
			newToken, tokenErr := c.tokenManager.RefreshToken(ctx)
			if tokenErr != nil {
				return "", fmt.Errorf("erro ao renovar o token: %w", tokenErr)
			}
			return requestFunc(newToken)
		}
		return "", err
	}
	return response, nil
}

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
