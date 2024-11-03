// llm/openai_client.go
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
	"time"
)

type OpenAIClient struct {
	apiKey string
	model  string
	logger *zap.Logger
	client *http.Client
}

func NewOpenAIClient(apiKey, model string, logger *zap.Logger) *OpenAIClient {
	// Configurar o cliente HTTP com LoggingTransport
	httpClient := utils.NewHTTPClient(logger, 30*time.Second)

	return &OpenAIClient{
		apiKey: apiKey,
		model:  model,
		logger: logger,
		client: httpClient,
	}
}

func (c *OpenAIClient) GetModelName() string {
	return c.model
}

func (c *OpenAIClient) SendPrompt(ctx context.Context, prompt string, history []models.Message) (string, error) {
	url := "https://api.openai.com/v1/chat/completions"

	// Construir o array de mensagens
	messages := []map[string]string{}

	// Adicionar o histórico
	for _, msg := range history {
		messages = append(messages, map[string]string{
			"role":    msg.Role,
			"content": msg.Content,
		})
	}

	// Adicionar a nova mensagem do usuário
	messages = append(messages, map[string]string{
		"role":    "user",
		"content": prompt,
	})

	payload := map[string]interface{}{
		"model":    c.model,
		"messages": messages,
	}

	jsonValue, err := json.Marshal(payload)
	if err != nil {
		c.logger.Error("Erro ao marshalizar o payload", zap.Error(err))
		return "", fmt.Errorf("erro ao preparar a requisição: %w", err)
	}

	maxAttempts := 3
	backoff := time.Second

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(jsonValue))
		if err != nil {
			c.logger.Error("Erro ao criar a requisição", zap.Error(err))
			return "", fmt.Errorf("erro ao criar a requisição: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+c.apiKey)

		startTime := time.Now()
		resp, err := c.client.Do(req)
		duration := time.Since(startTime)

		if err != nil {
			if utils.IsTemporaryError(err) {
				c.logger.Warn("Erro temporário ao chamar OpenAI",
					zap.Int("attempt", attempt),
					zap.Error(err),
					zap.Duration("duração", duration),
				)
				if attempt < maxAttempts {
					time.Sleep(backoff)
					backoff *= 2 // Backoff exponencial
					continue
				}
			}
			c.logger.Error("Erro ao fazer a requisição para OpenAI", zap.Error(err))
			return "", fmt.Errorf("erro ao fazer a requisição para OpenAI: %w", err)
		}
		defer resp.Body.Close()

		bodyBytes, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			c.logger.Error("Erro ao ler a resposta da OpenAI", zap.Error(err))
			return "", fmt.Errorf("erro ao ler a resposta da OpenAI: %w", err)
		}

		if resp.StatusCode != http.StatusOK {
			errMsg := fmt.Sprintf("Erro na requisição à OpenAI: status %d, resposta: %s", resp.StatusCode, string(bodyBytes))
			c.logger.Error("Resposta de erro da OpenAI",
				zap.Int("status", resp.StatusCode),
				zap.String("resposta", string(bodyBytes)),
				zap.Duration("duração", duration),
				zap.Int("tentativas", attempt),
			)
			return "", fmt.Errorf(errMsg)
		}

		var result map[string]interface{}
		if err := json.Unmarshal(bodyBytes, &result); err != nil {
			c.logger.Error("Erro ao decodificar a resposta da OpenAI", zap.Error(err))
			return "", fmt.Errorf("erro ao decodificar a resposta da OpenAI: %w", err)
		}

		choices, ok := result["choices"].([]interface{})
		if !ok || len(choices) == 0 {
			c.logger.Error("Nenhuma resposta recebida da OpenAI", zap.Any("resultado", result))
			return "", fmt.Errorf("nenhuma resposta recebida da OpenAI")
		}

		firstChoice, ok := choices[0].(map[string]interface{})
		if !ok {
			c.logger.Error("Formato inesperado no primeiro choice", zap.Any("choice", choices[0]))
			return "", fmt.Errorf("formato inesperado na resposta da OpenAI")
		}

		message, ok := firstChoice["message"].(map[string]interface{})
		if !ok {
			c.logger.Error("Formato inesperado na mensagem", zap.Any("message", firstChoice["message"]))
			return "", fmt.Errorf("formato inesperado na resposta da OpenAI")
		}

		content, ok := message["content"].(string)
		if !ok {
			c.logger.Error("Conteúdo da mensagem não é uma string", zap.Any("content", message["content"]))
			return "", fmt.Errorf("conteúdo da mensagem não é válido")
		}

		c.logger.Info("Resposta recebida da OpenAI",
			zap.String("prompt", prompt),
			zap.String("resposta", content),
			zap.Duration("duração", duration),
			zap.Int("tentativas", attempt),
		)

		return content, nil
	}

	errMsg := fmt.Sprintf("Falha ao obter resposta da OpenAI após %d tentativas", maxAttempts)
	c.logger.Error("Falha após múltiplas tentativas", zap.Int("tentativas", maxAttempts))
	return "", fmt.Errorf(errMsg)
}
