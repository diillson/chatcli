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
	"io"
	"net/http"
	"time"
)

type OpenAIClient struct {
	apiKey      string
	model       string
	logger      *zap.Logger
	client      *http.Client
	maxAttempts int
	backoff     time.Duration
}

// NewOpenAIClient cria uma nova instância de OpenAIClient.
// Recebe a chave da API, o modelo, o logger, o número máximo de tentativas e o tempo de backoff.
func NewOpenAIClient(apiKey, model string, logger *zap.Logger, maxAttempts int, backoff time.Duration) *OpenAIClient {
	// Configurar o cliente HTTP com LoggingTransport
	httpClient := utils.NewHTTPClient(logger, 30*time.Second)

	return &OpenAIClient{
		apiKey:      apiKey,
		model:       model,
		logger:      logger,
		client:      httpClient,
		maxAttempts: maxAttempts,
		backoff:     backoff,
	}
}

// GetModelName retorna o nome do modelo de linguagem utilizado pelo cliente.
func (c *OpenAIClient) GetModelName() string {
	return c.model
}

// SendPrompt envia um prompt para o modelo de linguagem e retorna a resposta.
// O contexto (ctx) pode ser usado para controlar o tempo de execução e cancelamento.
// O histórico (history) contém as mensagens anteriores da conversa.
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

	for attempt := 1; attempt <= c.maxAttempts; attempt++ {
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
				if attempt < c.maxAttempts {
					c.logger.Warn("Aplicando backoff antes de nova tentativa",
						zap.Int("attempt", attempt),
						zap.Duration("backoff", c.backoff),
					)
					time.Sleep(c.backoff)
					c.backoff *= 2 // Backoff exponencial
					continue
				}
			}
			c.logger.Error("Erro ao fazer a requisição para OpenAI", zap.Error(err))
			return "", fmt.Errorf("erro ao fazer a requisição para OpenAI: %w", err)
		}
		defer resp.Body.Close()

		bodyBytes, err := io.ReadAll(resp.Body)
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

		// Verificar se o campo "message" está presente
		if message, ok := firstChoice["message"].(map[string]interface{}); ok {
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
		} else {
			c.logger.Error("Campo 'message' ausente na resposta", zap.Any("choice", firstChoice))
			return "", fmt.Errorf("campo 'message' ausente na resposta da OpenAI")
		}
	}

	errMsg := fmt.Sprintf("Falha ao obter resposta da OpenAI após %d tentativas", c.maxAttempts)
	c.logger.Error("Falha após múltiplas tentativas", zap.Int("tentativas", c.maxAttempts))
	return "", fmt.Errorf(errMsg)
}
