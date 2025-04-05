package session

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/diillson/chatcli/config"
	"github.com/diillson/chatcli/utils"
	"go.uber.org/zap"
	"io"
	"net/http"
	"time"
)

const (
	claudeConversationsURL = "https://api.anthropic.com/v1/messages"
)

// ClaudeSession implementa o gerenciador de sessões para a API Claude Messages
type ClaudeSession struct {
	apiKey         string
	model          string
	logger         *zap.Logger
	client         *http.Client
	conversationID string
	active         bool
	maxAttempts    int
	backoff        time.Duration
	maxTokens      int
}

// NewClaudeSession cria uma nova sessão do Claude
func NewClaudeSession(apiKey string, model string, logger *zap.Logger, maxAttempts int, backoff time.Duration, maxTokens int) *ClaudeSession {
	httpClient := utils.NewHTTPClient(logger, 300*time.Second)
	if maxAttempts <= 0 {
		maxAttempts = config.ClaudeAIDefaultMaxAttempts
	}
	if backoff <= 0 {
		backoff = config.ClaudeAIDefaultBackoff
	}
	if maxTokens <= 0 {
		maxTokens = config.ClaudeAIDefaultMaxTokens
	}

	return &ClaudeSession{
		apiKey:      apiKey,
		model:       model,
		logger:      logger,
		client:      httpClient,
		maxAttempts: maxAttempts,
		backoff:     backoff,
		maxTokens:   maxTokens,
	}
}

// InitializeSession inicializa uma nova sessão do Claude
// Para o Claude, não precisa criar explicitamente uma conversação,
// apenas marcamos a sessão como ativa
func (s *ClaudeSession) InitializeSession(ctx context.Context) error {
	s.logger.Info("Inicializando sessão Claude Messages API")

	// Para o Claude, não criamos um ID de conversação inicialmente
	// Ele será retornado na primeira resposta
	s.active = true

	s.logger.Info("Sessão Claude inicializada com sucesso")
	return nil
}

// SendMessage envia uma mensagem na conversação do Claude
func (s *ClaudeSession) SendMessage(ctx context.Context, message string) (string, error) {
	if !s.active {
		return "", fmt.Errorf("sessão não está ativa; inicialize primeiro")
	}

	// Implementar exponential backoff
	var backoff = s.backoff
	var lastError error

	for attempt := 1; attempt <= s.maxAttempts; attempt++ {
		response, err := s.sendMessageAttempt(ctx, message)
		if err == nil {
			return response, nil
		}

		lastError = err
		s.logger.Warn("Tentativa de envio de mensagem falhou",
			zap.Int("attempt", attempt),
			zap.Error(err),
			zap.Duration("backoff", backoff))

		// Se ainda temos tentativas, espere antes da próxima
		if attempt < s.maxAttempts {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(backoff):
				// Aumentar o backoff para a próxima tentativa
				backoff *= 2
			}
		}
	}

	return "", fmt.Errorf("todas as tentativas falharam: %w", lastError)
}

// sendMessageAttempt é uma tentativa única de enviar uma mensagem
func (s *ClaudeSession) sendMessageAttempt(ctx context.Context, message string) (string, error) {
	// Estrutura básica do corpo da requisição
	requestBody := map[string]interface{}{
		"model":      s.model,
		"max_tokens": s.maxTokens,
		"messages": []map[string]string{
			{
				"role":    "user",
				"content": message,
			},
		},
	}

	// Adiciona o ID da conversação apenas se já tivermos um
	// e só se não estivermos na primeira mensagem
	if s.conversationID != "" {
		// Usar apenas system para continuar uma conversa
		requestBody["system"] = fmt.Sprintf("This is a continuation of a previous conversation with id %s.", s.conversationID)
	}

	jsonData, err := json.Marshal(requestBody)
	if err != nil {
		return "", fmt.Errorf("erro ao serializar mensagem: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, claudeConversationsURL, bytes.NewReader(jsonData))
	if err != nil {
		return "", fmt.Errorf("erro ao criar requisição: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", s.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := s.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("erro ao enviar requisição: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("erro ao ler resposta: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("erro na API: status %d, resposta: %s", resp.StatusCode, string(body))
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("erro ao deserializar resposta: %w", err)
	}

	// Se houver um ID de conversação na resposta, atualizar o nosso
	if convID, ok := result["conversation_id"].(string); ok && convID != "" {
		s.logger.Info("Atualizado ID de conversação do Claude", zap.String("conversation_id", convID))
		s.conversationID = convID
	}

	// Extrair a resposta do assistente
	content, ok := result["content"].([]interface{})
	if !ok || len(content) == 0 {
		return "", fmt.Errorf("conteúdo da resposta não encontrado")
	}

	var responseText string
	for _, item := range content {
		contentItem, ok := item.(map[string]interface{})
		if !ok {
			continue
		}

		if contentType, ok := contentItem["type"].(string); ok && contentType == "text" {
			if text, ok := contentItem["text"].(string); ok {
				responseText += text
			}
		}
	}

	if responseText == "" {
		return "", fmt.Errorf("texto da resposta não encontrado")
	}

	return responseText, nil
}

// GetSessionID retorna o ID da sessão (conversationID)
func (s *ClaudeSession) GetSessionID() string {
	return s.conversationID
}

// IsSessionActive verifica se a sessão está ativa
func (s *ClaudeSession) IsSessionActive() bool {
	return s.active
}

// EndSession encerra a sessão atual
// Similar à OpenAI, apenas marcamos como inativo localmente
func (s *ClaudeSession) EndSession(ctx context.Context) error {
	s.active = false
	s.conversationID = ""
	return nil
}

// GetModelName retorna o nome do modelo usado
func (s *ClaudeSession) GetModelName() string {
	return s.model
}
