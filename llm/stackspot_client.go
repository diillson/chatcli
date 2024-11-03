// llm/stackspot_client.go
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/utils"
	"go.uber.org/zap"
	"io/ioutil"
	"net/http"
	"strings"
	"time"
)

type StackSpotClient struct {
	tokenManager *TokenManager
	slug         string
	logger       *zap.Logger
	client       *http.Client
}

func NewStackSpotClient(tokenManager *TokenManager, slug string, logger *zap.Logger) *StackSpotClient {
	// Utilizar a função auxiliar para criar o cliente HTTP
	httpClient := utils.NewHTTPClient(logger, 30*time.Second)

	return &StackSpotClient{
		tokenManager: tokenManager,
		slug:         slug,
		logger:       logger,
		client:       httpClient,
	}
}

func (c *StackSpotClient) GetModelName() string {
	return "StackSpotAI"
}

// Função para formatar o histórico da conversa
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

func (c *StackSpotClient) SendPrompt(ctx context.Context, prompt string, history []models.Message) (string, error) {
	token, err := c.tokenManager.GetAccessToken(ctx)
	if err != nil {
		c.logger.Error("Erro ao obter o token", zap.Error(err))
		return "", fmt.Errorf("erro ao obter o token: %w", err)
	}

	// Formatar o histórico da conversa
	conversationHistory := formatConversationHistory(history)

	// Concatenar o histórico com o prompt atual
	fullPrompt := fmt.Sprintf("%sUsuário: %s", conversationHistory, prompt)

	// Enviar o prompt completo e obter o responseID
	responseID, err := c.sendRequestToLLMWithRetry(ctx, fullPrompt, token)
	if err != nil {
		c.logger.Error("Erro ao enviar a requisição para a LLM", zap.Error(err))
		return "", fmt.Errorf("erro ao enviar a requisição: %w", err)
	}

	var llmResponse string
	maxAttempts := 50
	for i := 0; i < maxAttempts; i++ {
		select {
		case <-ctx.Done():
			c.logger.Warn("Contexto cancelado ou expirado", zap.Error(ctx.Err()))
			return "", fmt.Errorf("contexto cancelado ou expirado: %w", ctx.Err())
		case <-time.After(2 * time.Second):
			llmResponse, err = c.getLLMResponseWithRetry(ctx, responseID, token)
			if err == nil {
				c.logger.Info("Resposta obtida com sucesso", zap.Int("tentativas", i+1))
				return llmResponse, nil
			}

			if strings.Contains(err.Error(), "resposta ainda não está pronta") {
				c.logger.Info("Resposta ainda não está pronta", zap.Int("tentativa", i+1))
				continue
			}

			if strings.Contains(err.Error(), "a execução da LLM falhou") {
				c.logger.Error("Falha na execução da LLM", zap.Error(err))
				return "", fmt.Errorf("a LLM não pôde processar a solicitação")
			}

			c.logger.Error("Erro ao obter a resposta da LLM", zap.Error(err))
			return "", fmt.Errorf("erro ao obter a resposta: %w", err)
		}
	}

	c.logger.Error("Timeout ao obter a resposta da LLM após todas as tentativas")
	return "", fmt.Errorf("timeout ao obter a resposta da LLM")
}

// Implementação das funções auxiliares com retry

func (c *StackSpotClient) sendRequestToLLMWithRetry(ctx context.Context, prompt, accessToken string) (string, error) {
	maxAttempts := 5
	backoff := time.Second

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		responseID, err := c.sendRequestToLLM(ctx, prompt, accessToken)
		if err != nil {
			if utils.IsTemporaryError(err) {
				c.logger.Warn("Erro temporário ao enviar requisição para StackSpotAI",
					zap.Int("attempt", attempt),
					zap.Error(err),
					zap.Duration("backoff", backoff),
				)
				if attempt < maxAttempts {
					time.Sleep(backoff)
					backoff *= 2 // Backoff exponencial
					continue
				}
			}
			return "", fmt.Errorf("erro ao enviar requisição para StackSpotAI: %w", err)
		}
		return responseID, nil
	}

	return "", fmt.Errorf("falha ao enviar requisição para StackSpotAI após %d tentativas", maxAttempts)
}

func (c *StackSpotClient) getLLMResponseWithRetry(ctx context.Context, responseID, accessToken string) (string, error) {
	maxAttempts := 3
	backoff := time.Second

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		llmResponse, err := c.getLLMResponse(ctx, responseID, accessToken)
		if err != nil {
			if utils.IsTemporaryError(err) {
				c.logger.Warn("Erro temporário ao obter resposta da StackSpotAI",
					zap.Int("attempt", attempt),
					zap.Error(err),
					zap.Duration("backoff", backoff),
				)
				if attempt < maxAttempts {
					time.Sleep(backoff)
					backoff *= 2 // Backoff exponencial
					continue
				}
			}
			return "", fmt.Errorf("erro ao obter resposta da StackSpotAI: %w", err)
		}
		return llmResponse, nil
	}

	return "", fmt.Errorf("falha ao obter resposta da StackSpotAI após %d tentativas", maxAttempts)
}

func (c *StackSpotClient) sendRequestToLLM(ctx context.Context, prompt, accessToken string) (string, error) {
	conversationID := generateUUID()

	url := fmt.Sprintf("https://genai-code-buddy-api.stackspot.com/v1/quick-commands/create-execution/%s?conversation_id=%s", c.slug, conversationID)
	c.logger.Info("Fazendo POST para URL", zap.String("url", url))

	requestBody := map[string]string{
		"input_data": prompt,
	}
	jsonValue, err := json.Marshal(requestBody)
	if err != nil {
		c.logger.Error("Erro ao marshalizar o corpo da requisição", zap.Error(err))
		return "", fmt.Errorf("erro ao preparar a requisição: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(jsonValue))
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
	defer resp.Body.Close()

	bodyBytes, err := ioutil.ReadAll(resp.Body)
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

func (c *StackSpotClient) getLLMResponse(ctx context.Context, responseID, accessToken string) (string, error) {
	url := fmt.Sprintf("https://genai-code-buddy-api.stackspot.com/v1/quick-commands/callback/%s", responseID)
	c.logger.Info("Fazendo GET para URL", zap.String("url", url))

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
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
	defer resp.Body.Close()

	bodyBytes, err := ioutil.ReadAll(resp.Body)
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
			lastStepIndex := len(callbackResponse.Steps) - 1
			lastStep := callbackResponse.Steps[lastStepIndex]
			llmAnswer := lastStep.StepResult.Answer
			return llmAnswer, nil
		} else {
			c.logger.Error("Nenhuma resposta disponível na execução COMPLETED", zap.Any("CallbackResponse", callbackResponse))
			return "", fmt.Errorf("nenhuma resposta disponível")
		}
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

// Função para gerar um UUID (para fins de exemplo, substitua por uma implementação real)
func generateUUID() string {
	return fmt.Sprintf("%d", time.Now().UnixNano())
}
