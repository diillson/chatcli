/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
 */
package openai_assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/utils"
	"go.uber.org/zap"
)

const (
	MaxAssistantFiles       = 20
	MaxFileSizeBytes        = 512 * 1024 * 1024 // 512MB
	AssistantAPIBaseURL     = "https://api.openai.com/v1"
	DefaultPollingInterval  = 1 * time.Second
	MaxPollingInterval      = 5 * time.Second
	DefaultPollingTimeout   = 5 * time.Minute
	DefaultAssistantModel   = "gpt-4o"
	DefaultAssistantName    = "ChatCLI Assistant"
	DefaultAssistantTimeout = 10 * time.Minute
)

// OpenAIAssistantClient implementa a interface LLMClient usando a API de Assistentes da OpenAI
type OpenAIAssistantClient struct {
	apiKey          string
	model           string
	assistantID     string
	currentThreadID string
	logger          *zap.Logger
	client          *utils.APIClient
	fileRegistry    *FileRegistry
	assistantName   string
	pollingInterval time.Duration
	pollingTimeout  time.Duration
	activeThreads   map[string]time.Time
	mu              sync.RWMutex
	fileUploadSem   chan struct{} // Semáforo para limitar uploads paralelos
}

// FileRegistry gerencia o cache de arquivos já enviados para a OpenAI
type FileRegistry struct {
	Files       map[string]string // Mapeia caminhos locais para IDs de arquivo na OpenAI
	TotalSize   int64             // Tamanho total dos arquivos carregados
	mu          sync.RWMutex
	logger      *zap.Logger
	cachePath   string
	assistantID string
}

// NewOpenAIAssistantClient cria uma nova instância de OpenAIAssistantClient
func NewOpenAIAssistantClient(apiKey, model string, logger *zap.Logger) (*OpenAIAssistantClient, error) {
	if model == "" {
		model = DefaultAssistantModel
	}

	client := utils.NewAPIClient(
		logger,
		AssistantAPIBaseURL,
		map[string]string{
			"Authorization": "Bearer " + apiKey,
			"Content-Type":  "application/json",
			"OpenAI-Beta":   "assistants=v2",
		},
	)

	// Criar o diretório de cache se não existir
	cacheDir := filepath.Join(os.TempDir(), "chatcli-openai-cache")
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		return nil, fmt.Errorf("erro ao criar diretório de cache: %w", err)
	}

	assistantClient := &OpenAIAssistantClient{
		apiKey:          apiKey,
		model:           model,
		assistantID:     "",
		currentThreadID: "",
		logger:          logger,
		client:          client,
		fileRegistry:    newFileRegistry(logger, cacheDir),
		assistantName:   DefaultAssistantName,
		pollingInterval: DefaultPollingInterval,
		pollingTimeout:  DefaultPollingTimeout,
		activeThreads:   make(map[string]time.Time),
		fileUploadSem:   make(chan struct{}, 3), // Limitar a 3 uploads paralelos
	}

	// Inicializar o assistente
	if err := assistantClient.initializeAssistant(); err != nil {
		return nil, err
	}

	return assistantClient, nil
}

// GetModelName retorna o nome do modelo utilizado
func (c *OpenAIAssistantClient) GetModelName() string {
	return fmt.Sprintf("%s (Assistant)", c.model)
}

// SendPrompt envia uma mensagem para o thread atual e retorna a resposta
func (c *OpenAIAssistantClient) SendPrompt(ctx context.Context, prompt string, history []models.Message, maxTokens int) (string, error) {
	c.mu.Lock()

	// Verificar se já temos um thread ativo, se não, criar um novo
	if c.currentThreadID == "" {
		threadID, err := c.createThread(ctx)
		if err != nil {
			c.mu.Unlock()
			return "", fmt.Errorf("erro ao criar thread: %w", err)
		}
		c.currentThreadID = threadID
		c.activeThreads[threadID] = time.Now()
		c.logger.Info("Thread criada", zap.String("threadID", threadID))
	}

	threadID := c.currentThreadID
	c.mu.Unlock()

	// Adicionar a mensagem ao thread
	if err := c.addMessageToThread(ctx, threadID, prompt); err != nil {
		c.logger.Error("Erro ao adicionar mensagem ao thread",
			zap.String("threadID", threadID),
			zap.Error(err))
		return "", fmt.Errorf("erro ao adicionar mensagem ao thread: %w", err)
	}

	// Executar o assistente no thread
	runID, err := c.runAssistant(ctx, threadID)
	if err != nil {
		c.logger.Error("Erro ao executar o assistente",
			zap.String("threadID", threadID),
			zap.Error(err))
		return "", fmt.Errorf("erro ao executar o assistente: %w", err)
	}

	c.logger.Debug("Assistente executando",
		zap.String("threadID", threadID),
		zap.String("runID", runID))

	// Aguardar a conclusão da execução
	runStatus, err := c.waitForRunCompletion(ctx, threadID, runID)
	if err != nil {
		c.logger.Error("Erro ao aguardar resposta",
			zap.String("threadID", threadID),
			zap.String("runID", runID),
			zap.Error(err))

		// Se for um erro de timeout, podemos tentar recuperar parcialmente
		if strings.Contains(err.Error(), "timeout") {
			c.logger.Warn("Tentando recuperar mensagens após timeout",
				zap.String("threadID", threadID))

			// Tentar obter a última resposta mesmo com timeout
			partialResponse, getErr := c.getLatestResponse(ctx, threadID)
			if getErr == nil && partialResponse != "" {
				c.logger.Info("Resposta parcial recuperada com sucesso após timeout")
				return fmt.Sprintf("[Resposta parcial devido a timeout] %s", partialResponse), nil
			}
		}

		return "", fmt.Errorf("erro ao aguardar resposta: %w", err)
	}

	if runStatus != "completed" {
		c.logger.Error("Execução do assistente falhou",
			zap.String("threadID", threadID),
			zap.String("runID", runID),
			zap.String("status", runStatus))
		return "", fmt.Errorf("execução do assistente falhou: %s", runStatus)
	}

	// Obter a resposta
	response, err := c.getLatestResponse(ctx, threadID)
	if err != nil {
		c.logger.Error("Erro ao obter resposta",
			zap.String("threadID", threadID),
			zap.Error(err))
		return "", fmt.Errorf("erro ao obter resposta: %w", err)
	}

	return response, nil
}

// Método para limpar threads ao fim da app
func (c *OpenAIAssistantClient) Cleanup() error {
	c.mu.RLock()
	threadID := c.currentThreadID
	c.mu.RUnlock()

	if threadID == "" {
		return nil // Nada para limpar
	}

	// Criar contexto com timeout para a limpeza
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Tenta finalizar qualquer run ativo na thread
	c.finishActiveRuns(ctx, threadID)

	c.logger.Info("Limpeza de recursos do OpenAI Assistant realizada")
	return nil
}

// finishActiveRuns tenta encontrar e finalizar runs ativos em uma thread
func (c *OpenAIAssistantClient) finishActiveRuns(ctx context.Context, threadID string) {
	// Listar runs ativos
	endpoint := fmt.Sprintf("/threads/%s/runs?limit=10", threadID)
	resp, err := c.client.Get(ctx, endpoint)
	if err != nil {
		c.logger.Warn("Erro ao listar runs para limpeza", zap.Error(err))
		return
	}

	var runsResponse struct {
		Data []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"data"`
	}

	if err := json.Unmarshal(resp, &runsResponse); err != nil {
		c.logger.Warn("Erro ao decodificar resposta de runs", zap.Error(err))
		return
	}

	// Cancelar runs ativos
	for _, run := range runsResponse.Data {
		if run.Status == "in_progress" || run.Status == "queued" {
			cancelEndpoint := fmt.Sprintf("/threads/%s/runs/%s/cancel", threadID, run.ID)
			_, err := c.client.Post(ctx, cancelEndpoint, nil)
			if err != nil {
				c.logger.Warn("Erro ao cancelar run",
					zap.String("runID", run.ID),
					zap.Error(err))
			} else {
				c.logger.Info("Run cancelado com sucesso", zap.String("runID", run.ID))
			}
		}
	}
}

// initializeAssistant cria ou recupera um assistente OpenAI
func (c *OpenAIAssistantClient) initializeAssistant() error {
	// Tentar carregar assistantID do cache
	assistantID, err := c.loadAssistantIDFromCache()
	if err == nil && assistantID != "" {
		// Verificar se o assistente ainda existe
		if c.verifyAssistantExists(context.Background(), assistantID) {
			c.assistantID = assistantID
			c.logger.Info("Assistente recuperado do cache", zap.String("assistantID", assistantID))
			return nil
		}
	}

	// Criar um novo assistente
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	payload := map[string]interface{}{
		"model":       c.model,
		"name":        c.assistantName,
		"description": "ChatCLI Assistant para análise de código e assistência em desenvolvimento",
		"instructions": "Você é um assistente especializado em desenvolvimento de software. " +
			"Analise arquivos de código com atenção aos detalhes, fornecendo explicações " +
			"claras e sugestões de melhorias. Considere boas práticas de programação, " +
			"padrões de projeto e possíveis problemas de segurança.",
		"tools": []map[string]string{
			{"type": "code_interpreter"},
			{"type": "file_search"},
		},
	}

	resp, err := c.client.Post(ctx, "/assistants", payload)
	if err != nil {
		return fmt.Errorf("erro ao criar assistente: %w", err)
	}

	var assistant struct {
		ID string `json:"id"`
	}

	if err := json.Unmarshal(resp, &assistant); err != nil {
		return fmt.Errorf("erro ao decodificar resposta do assistente: %w", err)
	}

	c.assistantID = assistant.ID

	// Salvar no cache
	if err := c.saveAssistantIDToCache(assistant.ID); err != nil {
		c.logger.Warn("Não foi possível salvar assistantID no cache", zap.Error(err))
	}

	c.logger.Info("Novo assistente criado", zap.String("assistantID", assistant.ID))
	return nil
}

// verifyAssistantExists verifica se um assistente ainda existe na API
func (c *OpenAIAssistantClient) verifyAssistantExists(ctx context.Context, assistantID string) bool {
	_, err := c.client.Get(ctx, fmt.Sprintf("/assistants/%s", assistantID))
	return err == nil
}

// loadAssistantIDFromCache carrega o ID do assistente do cache
func (c *OpenAIAssistantClient) loadAssistantIDFromCache() (string, error) {
	cacheFile := filepath.Join(os.TempDir(), "chatcli-openai-cache", "assistant.json")
	data, err := os.ReadFile(cacheFile)
	if err != nil {
		return "", err
	}

	var cache struct {
		AssistantID string `json:"assistant_id"`
		Model       string `json:"model"`
	}

	if err := json.Unmarshal(data, &cache); err != nil {
		return "", err
	}

	// Verificar se o modelo mudou - se sim, criar novo assistente
	if cache.Model != c.model {
		return "", fmt.Errorf("modelo diferente do cache")
	}

	return cache.AssistantID, nil
}

// saveAssistantIDToCache salva o ID do assistente no cache
func (c *OpenAIAssistantClient) saveAssistantIDToCache(assistantID string) error {
	cacheDir := filepath.Join(os.TempDir(), "chatcli-openai-cache")
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		return err
	}

	cacheFile := filepath.Join(cacheDir, "assistant.json")
	cache := struct {
		AssistantID string `json:"assistant_id"`
		Model       string `json:"model"`
	}{
		AssistantID: assistantID,
		Model:       c.model,
	}

	data, err := json.Marshal(cache)
	if err != nil {
		return err
	}

	return os.WriteFile(cacheFile, data, 0644)
}

// newFileRegistry cria um novo registro de arquivos
func newFileRegistry(logger *zap.Logger, cacheDir string) *FileRegistry {
	cachePath := filepath.Join(cacheDir, "file_registry.json")

	registry := &FileRegistry{
		Files:     make(map[string]string),
		TotalSize: 0,
		logger:    logger,
		cachePath: cachePath,
	}

	// Carregar cache existente, se houver
	registry.loadCache()

	return registry
}

// AddFile adiciona um arquivo ao registro se ele já não existir
func (r *FileRegistry) AddFile(filePath, fileID string, fileSize int64) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.Files[filePath]; !exists {
		r.Files[filePath] = fileID
		r.TotalSize += fileSize
		r.saveCache()
	}
}

// GetFileID retorna o ID do arquivo se estiver no registro
func (r *FileRegistry) GetFileID(filePath string) (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	id, exists := r.Files[filePath]
	return id, exists
}

// loadCache carrega o cache do disco
func (r *FileRegistry) loadCache() {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Verificar se o arquivo de cache existe
	if _, err := os.Stat(r.cachePath); os.IsNotExist(err) {
		r.logger.Info("Arquivo de cache não encontrado, criando novo registro",
			zap.String("path", r.cachePath))
		return
	}

	// Abrir e ler o arquivo
	data, err := os.ReadFile(r.cachePath)
	if err != nil {
		r.logger.Warn("Erro ao ler arquivo de cache", zap.Error(err))
		return
	}

	// Decodificar o conteúdo
	var cache struct {
		Files       map[string]string `json:"files"`
		TotalSize   int64             `json:"total_size"`
		AssistantID string            `json:"assistant_id"`
		LastUpdated string            `json:"last_updated"`
	}

	if err := json.Unmarshal(data, &cache); err != nil {
		r.logger.Warn("Erro ao decodificar cache", zap.Error(err))
		return
	}

	// Verificar se os arquivos ainda existem
	validFiles := make(map[string]string)
	var validSize int64

	for path, id := range cache.Files {
		if fi, err := os.Stat(path); err == nil {
			validFiles[path] = id
			validSize += fi.Size()
		}
	}

	r.Files = validFiles
	r.TotalSize = validSize
	r.assistantID = cache.AssistantID

	r.logger.Info("Cache carregado com sucesso",
		zap.Int("file_count", len(r.Files)),
		zap.Int64("total_size", r.TotalSize))
}

// saveCache salva o cache no disco
func (r *FileRegistry) saveCache() {
	r.mu.RLock()
	defer r.mu.RUnlock()

	// Criar o diretório do cache se não existir
	cacheDir := filepath.Dir(r.cachePath)
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		r.logger.Warn("Erro ao criar diretório de cache",
			zap.String("dir", cacheDir),
			zap.Error(err))
		return
	}

	// Preparar os dados para salvar
	cache := struct {
		Files       map[string]string `json:"files"`
		TotalSize   int64             `json:"total_size"`
		AssistantID string            `json:"assistant_id"`
		LastUpdated string            `json:"last_updated"`
	}{
		Files:       r.Files,
		TotalSize:   r.TotalSize,
		AssistantID: r.assistantID,
		LastUpdated: time.Now().Format(time.RFC3339),
	}

	// Codificar os dados
	data, err := json.MarshalIndent(cache, "", "  ")
	if err != nil {
		r.logger.Warn("Erro ao codificar cache", zap.Error(err))
		return
	}

	// Salvar no arquivo
	if err := os.WriteFile(r.cachePath, data, 0644); err != nil {
		r.logger.Warn("Erro ao salvar cache", zap.Error(err))
		return
	}

	r.logger.Debug("Cache salvo com sucesso",
		zap.String("path", r.cachePath),
		zap.Int("file_count", len(r.Files)))
}

// Criar um thread
func (c *OpenAIAssistantClient) createThread(ctx context.Context) (string, error) {
	payload := map[string]interface{}{}

	resp, err := c.client.Post(ctx, "/threads", payload)
	if err != nil {
		return "", err
	}

	var response struct {
		ID string `json:"id"`
	}

	if err := json.Unmarshal(resp, &response); err != nil {
		return "", fmt.Errorf("erro ao decodificar resposta: %w", err)
	}

	return response.ID, nil
}

// Adicionar mensagem a um thread
func (c *OpenAIAssistantClient) addMessageToThread(ctx context.Context, threadID, content string) error {
	payload := map[string]interface{}{
		"role":    "user",
		"content": content,
	}

	_, err := c.client.Post(ctx, fmt.Sprintf("/threads/%s/messages", threadID), payload)
	return err
}

// Executar o assistente em um thread
func (c *OpenAIAssistantClient) runAssistant(ctx context.Context, threadID string) (string, error) {
	payload := map[string]interface{}{
		"assistant_id": c.assistantID,
	}

	resp, err := c.client.Post(ctx, fmt.Sprintf("/threads/%s/runs", threadID), payload)
	if err != nil {
		return "", err
	}

	var response struct {
		ID string `json:"id"`
	}

	if err := json.Unmarshal(resp, &response); err != nil {
		return "", fmt.Errorf("erro ao decodificar resposta: %w", err)
	}

	return response.ID, nil
}

// Aguardar pela conclusão da execução
// Aguardar pela conclusão da execução
func (c *OpenAIAssistantClient) waitForRunCompletion(ctx context.Context, threadID, runID string) (string, error) {
	interval := c.pollingInterval
	endTime := time.Now().Add(c.pollingTimeout)

	for time.Now().Before(endTime) {
		resp, err := c.client.Get(ctx, fmt.Sprintf("/threads/%s/runs/%s", threadID, runID))
		if err != nil {
			return "", err
		}

		var runStatus struct {
			Status string `json:"status"`
		}

		if err := json.Unmarshal(resp, &runStatus); err != nil {
			return "", fmt.Errorf("erro ao decodificar status: %w", err)
		}

		switch runStatus.Status {
		case "completed":
			return "completed", nil
		case "failed", "cancelled", "expired":
			return runStatus.Status, fmt.Errorf("execução falhou com status: %s", runStatus.Status)
		}

		// Backoff exponencial com limite máximo
		time.Sleep(interval)
		if interval < MaxPollingInterval {
			// Correção aqui: converter para float64, multiplicar, e converter de volta
			interval = time.Duration(float64(interval) * 1.5)
			if interval > MaxPollingInterval {
				interval = MaxPollingInterval
			}
		}
	}

	return "", fmt.Errorf("timeout ao aguardar conclusão")
}

// Obter a última resposta do assistente
func (c *OpenAIAssistantClient) getLatestResponse(ctx context.Context, threadID string) (string, error) {
	resp, err := c.client.Get(ctx, fmt.Sprintf("/threads/%s/messages?limit=1&order=desc", threadID))
	if err != nil {
		return "", err
	}

	var response struct {
		Data []struct {
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
				Text struct {
					Value string `json:"value"`
				} `json:"text"`
			} `json:"content"`
		} `json:"data"`
	}

	if err := json.Unmarshal(resp, &response); err != nil {
		return "", fmt.Errorf("erro ao decodificar mensagens: %w", err)
	}

	if len(response.Data) == 0 || response.Data[0].Role != "assistant" {
		return "", fmt.Errorf("nenhuma resposta do assistente encontrada")
	}

	var fullResponse strings.Builder
	for _, content := range response.Data[0].Content {
		if content.Type == "text" {
			fullResponse.WriteString(content.Text.Value)
		}
	}

	return fullResponse.String(), nil
}
