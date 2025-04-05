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
	openAIAssistantsURL   = "https://api.openai.com/v1/assistants"
	openAIThreadsURL      = "https://api.openai.com/v1/threads"
	openAIRunsURL         = "https://api.openai.com/v1/threads/%s/runs"
	openAIMessagesURL     = "https://api.openai.com/v1/threads/%s/messages"
	openAIThreadRunURL    = "https://api.openai.com/v1/threads/%s/runs/%s"
	openAIThreadMsgAddURL = "https://api.openai.com/v1/threads/%s/messages"
)

// OpenAISession implementa o gerenciador de sessões para a API Assistants da OpenAI
type OpenAISession struct {
	apiKey      string
	model       string
	logger      *zap.Logger
	client      *http.Client
	threadID    string
	assistantID string
	active      bool
	maxAttempts int
	backoff     time.Duration
}

// NewOpenAISession cria uma nova sessão da OpenAI
func NewOpenAISession(apiKey string, model string, logger *zap.Logger, maxAttempts int, backoff time.Duration) *OpenAISession {
	httpClient := utils.NewHTTPClient(logger, 300*time.Second)
	if maxAttempts <= 0 {
		maxAttempts = config.OpenAIDefaultMaxAttempts
	}
	if backoff <= 0 {
		backoff = config.OpenAIDefaultBackoff
	}

	return &OpenAISession{
		apiKey:      apiKey,
		model:       model,
		logger:      logger,
		client:      httpClient,
		maxAttempts: maxAttempts,
		backoff:     backoff,
	}
}

// InitializeSession cria um novo assistente e thread na API da OpenAI
func (s *OpenAISession) InitializeSession(ctx context.Context) error {
	s.logger.Info("Inicializando sessão OpenAI Assistants API")

	// Primeiro, criar um assistente baseado no modelo solicitado
	assistantID, err := s.createAssistant(ctx)
	if err != nil {
		return fmt.Errorf("falha ao criar assistente: %w", err)
	}
	s.assistantID = assistantID

	// Em seguida, criar um thread para a conversação
	threadID, err := s.createThread(ctx)
	if err != nil {
		return fmt.Errorf("falha ao criar thread: %w", err)
	}
	s.threadID = threadID

	s.active = true
	s.logger.Info("Sessão OpenAI inicializada com sucesso",
		zap.String("assistantID", s.assistantID),
		zap.String("threadID", s.threadID))
	return nil
}

// createAssistant cria um novo assistente na OpenAI
func (s *OpenAISession) createAssistant(ctx context.Context) (string, error) {
	requestBody := map[string]interface{}{
		"model": s.model,
		"name":  "ChatCLI Assistant",
		"instructions": "Você é um assistente de terminal inteligente e conciso. " +
			"Ajude com consultas de programação, explicações técnicas e outras tarefas. " +
			"Use formatação Markdown quando apropriado.",
	}

	jsonData, err := json.Marshal(requestBody)
	if err != nil {
		return "", fmt.Errorf("erro ao serializar dados do assistente: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, openAIAssistantsURL, bytes.NewReader(jsonData))
	if err != nil {
		return "", fmt.Errorf("erro ao criar requisição: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.apiKey)
	// Atualizado para v2 conforme erro da API
	req.Header.Set("OpenAI-Beta", "assistants=v2")

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

	assistantID, ok := result["id"].(string)
	if !ok {
		return "", fmt.Errorf("ID do assistente não encontrado na resposta")
	}

	return assistantID, nil
}

// createThread cria um novo thread na OpenAI
func (s *OpenAISession) createThread(ctx context.Context) (string, error) {
	// Para criar um thread vazio, enviamos um objeto JSON vazio
	requestBody := map[string]interface{}{}

	jsonData, err := json.Marshal(requestBody)
	if err != nil {
		return "", fmt.Errorf("erro ao serializar dados do thread: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, openAIThreadsURL, bytes.NewReader(jsonData))
	if err != nil {
		return "", fmt.Errorf("erro ao criar requisição: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.apiKey)
	// Atualizado para v2 conforme erro da API
	req.Header.Set("OpenAI-Beta", "assistants=v2")

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

	threadID, ok := result["id"].(string)
	if !ok {
		return "", fmt.Errorf("ID do thread não encontrado na resposta")
	}

	return threadID, nil
}

// SendMessage envia uma mensagem no thread e executa o assistente
func (s *OpenAISession) SendMessage(ctx context.Context, message string) (string, error) {
	if !s.active {
		return "", fmt.Errorf("sessão não está ativa; inicialize primeiro")
	}

	// Primeiro, adicionar a mensagem ao thread
	err := s.addMessageToThread(ctx, message)
	if err != nil {
		return "", fmt.Errorf("erro ao adicionar mensagem ao thread: %w", err)
	}

	// Em seguida, executar o assistente no thread
	runID, err := s.createRun(ctx)
	if err != nil {
		return "", fmt.Errorf("erro ao criar execução: %w", err)
	}

	// Aguardar pela conclusão da execução
	response, err := s.waitForRunCompletion(ctx, runID)
	if err != nil {
		return "", fmt.Errorf("erro ao aguardar conclusão da execução: %w", err)
	}

	return response, nil
}

// addMessageToThread adiciona uma mensagem do usuário ao thread
func (s *OpenAISession) addMessageToThread(ctx context.Context, content string) error {
	url := fmt.Sprintf(openAIThreadMsgAddURL, s.threadID)

	requestBody := map[string]interface{}{
		"role":    "user",
		"content": content,
	}

	jsonData, err := json.Marshal(requestBody)
	if err != nil {
		return fmt.Errorf("erro ao serializar mensagem: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(jsonData))
	if err != nil {
		return fmt.Errorf("erro ao criar requisição: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.apiKey)
	// Atualizado para v2 conforme erro da API
	req.Header.Set("OpenAI-Beta", "assistants=v2")

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("erro ao enviar requisição: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("erro ao ler resposta: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("erro na API: status %d, resposta: %s", resp.StatusCode, string(body))
	}

	return nil
}

// createRun cria uma execução do assistente no thread
func (s *OpenAISession) createRun(ctx context.Context) (string, error) {
	url := fmt.Sprintf(openAIRunsURL, s.threadID)

	requestBody := map[string]interface{}{
		"assistant_id": s.assistantID,
	}

	jsonData, err := json.Marshal(requestBody)
	if err != nil {
		return "", fmt.Errorf("erro ao serializar dados da execução: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(jsonData))
	if err != nil {
		return "", fmt.Errorf("erro ao criar requisição: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.apiKey)
	// Atualizado para v2 conforme erro da API
	req.Header.Set("OpenAI-Beta", "assistants=v2")

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

	runID, ok := result["id"].(string)
	if !ok {
		return "", fmt.Errorf("ID da execução não encontrado na resposta")
	}

	return runID, nil
}

// waitForRunCompletion aguarda a conclusão da execução e obtém a resposta
func (s *OpenAISession) waitForRunCompletion(ctx context.Context, runID string) (string, error) {
	url := fmt.Sprintf(openAIThreadRunURL, s.threadID, runID)

	// Poll a cada 1 segundo até a conclusão ou timeout
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-ticker.C:
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
			if err != nil {
				return "", fmt.Errorf("erro ao criar requisição: %w", err)
			}

			req.Header.Set("Authorization", "Bearer "+s.apiKey)
			// Atualizado para v2 conforme erro da API
			req.Header.Set("OpenAI-Beta", "assistants=v2")

			resp, err := s.client.Do(req)
			if err != nil {
				return "", fmt.Errorf("erro ao enviar requisição: %w", err)
			}

			body, err := io.ReadAll(resp.Body)
			resp.Body.Close()
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

			status, ok := result["status"].(string)
			if !ok {
				return "", fmt.Errorf("status da execução não encontrado na resposta")
			}

			// Verificar o status da execução
			switch status {
			case "completed":
				// Execução concluída com sucesso, obter mensagens
				return s.getLatestAssistantMessage(ctx)
			case "failed", "cancelled", "expired":
				return "", fmt.Errorf("execução falhou com status: %s", status)
			case "requires_action":
				return "", fmt.Errorf("execução requer ação (funcionalidade não suportada)")
			}
			// Se ainda estiver em execução, continue aguardando
		}
	}
}

// getLatestAssistantMessage obtém a mensagem mais recente do assistente no thread
func (s *OpenAISession) getLatestAssistantMessage(ctx context.Context) (string, error) {
	url := fmt.Sprintf(openAIMessagesURL, s.threadID)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("erro ao criar requisição: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+s.apiKey)
	// Atualizado para v2 conforme erro da API
	req.Header.Set("OpenAI-Beta", "assistants=v2")

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

	data, ok := result["data"].([]interface{})
	if !ok || len(data) == 0 {
		return "", fmt.Errorf("nenhuma mensagem encontrada no thread")
	}

	// Procura pela mensagem mais recente do assistente
	for _, msgData := range data {
		msg, ok := msgData.(map[string]interface{})
		if !ok {
			continue
		}

		role, ok := msg["role"].(string)
		if !ok || role != "assistant" {
			continue
		}

		// Extrair o conteúdo da mensagem
		contentItems, ok := msg["content"].([]interface{})
		if !ok || len(contentItems) == 0 {
			continue
		}

		var messageText string
		for _, item := range contentItems {
			contentItem, ok := item.(map[string]interface{})
			if !ok {
				continue
			}

			contentType, ok := contentItem["type"].(string)
			if !ok || contentType != "text" {
				continue
			}

			textValue, ok := contentItem["text"].(map[string]interface{})
			if !ok {
				continue
			}

			value, ok := textValue["value"].(string)
			if !ok {
				continue
			}

			messageText += value
		}

		if messageText != "" {
			return messageText, nil
		}
	}

	return "", fmt.Errorf("nenhuma mensagem do assistente encontrada")
}

// GetSessionID retorna o ID da sessão (threadID)
func (s *OpenAISession) GetSessionID() string {
	return s.threadID
}

// IsSessionActive verifica se a sessão está ativa
func (s *OpenAISession) IsSessionActive() bool {
	return s.active
}

// EndSession encerra a sessão atual
// Nota: A API da OpenAI não possui um endpoint específico para 'encerrar' threads,
// então marcamos apenas como inativo em nosso objeto
func (s *OpenAISession) EndSession(ctx context.Context) error {
	s.active = false
	s.threadID = ""
	s.assistantID = ""
	return nil
}

// GetModelName retorna o nome do modelo usado
func (s *OpenAISession) GetModelName() string {
	return s.model
}
