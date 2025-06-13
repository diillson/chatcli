package openai_assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"sort"
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
func (c *OpenAIAssistantClient) SendPrompt(ctx context.Context, prompt string, history []models.Message) (string, error) {
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

// Método para deletar thread
func (c *OpenAIAssistantClient) deleteThread(ctx context.Context, threadID string) error {
	endpoint := fmt.Sprintf("/threads/%s", threadID)
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, AssistantAPIBaseURL+endpoint, nil)
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("OpenAI-Beta", "assistants=v2")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("erro ao deletar thread: status %d, resposta: %s", resp.StatusCode, string(body))
	}

	c.logger.Info("Thread deletada com sucesso", zap.String("threadID", threadID))
	return nil
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

// attachFileToAssistant anexa um arquivo ao assistente
func (c *OpenAIAssistantClient) attachFileToAssistant(ctx context.Context, fileID string) error {
	payload := map[string]interface{}{
		"file_id": fileID,
	}

	endpoint := fmt.Sprintf("/assistants/%s/files", c.assistantID)
	_, err := c.client.Post(ctx, endpoint, payload)
	if err != nil {
		return fmt.Errorf("erro ao anexar arquivo ao assistente: %w", err)
	}

	c.logger.Info("Arquivo anexado ao assistente",
		zap.String("fileID", fileID),
		zap.String("assistantID", c.assistantID))
	return nil
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

// uploadFile carrega um arquivo para a OpenAI API
func (c *OpenAIAssistantClient) uploadFile(ctx context.Context, filePath string) (string, error) {
	// Verificar o cache primeiro
	if fileID, exists := c.fileRegistry.GetFileID(filePath); exists {
		c.logger.Info("Arquivo encontrado no cache",
			zap.String("path", filePath),
			zap.String("fileID", fileID))
		return fileID, nil
	}

	// Verificar se o contexto já expirou antes de iniciar o upload
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("contexto expirado antes de iniciar upload: %w", err)
	}

	// Limitar uploads concorrentes
	select {
	case c.fileUploadSem <- struct{}{}:
		// Conseguiu um slot no semáforo
	case <-ctx.Done():
		return "", fmt.Errorf("contexto cancelado enquanto aguardava slot para upload: %w", ctx.Err())
	}
	defer func() { <-c.fileUploadSem }()

	// Abrir o arquivo
	file, err := os.Open(filePath)
	if err != nil {
		return "", fmt.Errorf("erro ao abrir arquivo: %w", err)
	}
	defer file.Close()

	fileInfo, err := file.Stat()
	if err != nil {
		return "", fmt.Errorf("erro ao obter informações do arquivo: %w", err)
	}

	if fileInfo.Size() > MaxFileSizeBytes {
		return "", fmt.Errorf("arquivo muito grande: %s (%.2f MB, limite: 512MB)",
			filePath, float64(fileInfo.Size())/1024/1024)
	}

	// Verificar se é um arquivo binário ou não suportado
	ext := strings.ToLower(filepath.Ext(filePath))
	unsupportedExts := map[string]bool{
		".exe": true, ".dll": true, ".so": true, ".dylib": true,
		".zip": true, ".tar": true, ".gz": true, ".rar": true,
		".bin": true, ".dat": true, ".db": true, ".sqlite": true,
	}

	// Verificar se é um arquivo provavelmente binário pelo tamanho e extensão
	if unsupportedExts[ext] || (fileInfo.Size() > 1024*1024 && !isTextFile(filePath)) {
		c.logger.Warn("Pulando arquivo potencialmente binário ou não suportado",
			zap.String("path", filePath),
			zap.String("ext", ext),
			zap.Int64("size", fileInfo.Size()))
		return "", fmt.Errorf("arquivo parece ser binário ou não suportado: %s", filePath)
	}

	// Preparar o multipart/form-data
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	// Adicionar o arquivo
	part, err := writer.CreateFormFile("file", filepath.Base(filePath))
	if err != nil {
		return "", fmt.Errorf("erro ao criar form file: %w", err)
	}

	// Copiar o conteúdo do arquivo para o form-data
	if _, err = io.Copy(part, file); err != nil {
		return "", fmt.Errorf("erro ao copiar arquivo: %w", err)
	}

	// Adicionar o propósito "assistants"
	if err = writer.WriteField("purpose", "assistants"); err != nil {
		return "", fmt.Errorf("erro ao definir propósito: %w", err)
	}

	// Finalizar o writer multipart/form-data
	if err = writer.Close(); err != nil {
		return "", fmt.Errorf("erro ao finalizar multipart writer: %w", err)
	}

	// Criar a requisição
	req, err := http.NewRequestWithContext(ctx, "POST", AssistantAPIBaseURL+"/files", body)
	if err != nil {
		return "", fmt.Errorf("erro ao criar requisição: %w", err)
	}

	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	// Enviar a requisição
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("erro ao enviar requisição: %w", err)
	}
	defer resp.Body.Close()

	// Ler a resposta
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("erro ao ler resposta: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("erro ao carregar arquivo (status %d): %s",
			resp.StatusCode, string(respBody))
	}

	// Decodificar a resposta
	var result struct {
		ID string `json:"id"`
	}

	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("erro ao decodificar resposta: %w", err)
	}

	fileID := result.ID

	// Salvar no cache
	c.fileRegistry.AddFile(filePath, fileID, fileInfo.Size())

	c.logger.Info("Arquivo carregado com sucesso",
		zap.String("path", filePath),
		zap.String("fileID", fileID),
		zap.Int64("size", fileInfo.Size()))

	return fileID, nil
}

// Função auxiliar para verificar se um arquivo parece ser texto
func isTextFile(filePath string) bool {
	// Lista de extensões conhecidas por serem texto
	textExts := map[string]bool{
		".txt": true, ".md": true, ".js": true, ".ts": true, ".py": true,
		".go": true, ".java": true, ".c": true, ".cpp": true, ".h": true,
		".cs": true, ".php": true, ".html": true, ".css": true, ".json": true,
		".xml": true, ".yaml": true, ".yml": true, ".sh": true, ".bash": true,
		".sql": true, ".conf": true, ".ini": true, ".toml": true, ".rs": true,
	}

	ext := strings.ToLower(filepath.Ext(filePath))
	if textExts[ext] {
		return true
	}

	// Para arquivos sem extensão reconhecida, verificar o conteúdo
	file, err := os.Open(filePath)
	if err != nil {
		return false
	}
	defer file.Close()

	// Ler os primeiros 512 bytes para detecção de tipo
	buffer := make([]byte, 512)
	n, err := file.Read(buffer)
	if err != nil && err != io.EOF {
		return false
	}

	// Verificar se há bytes nulos ou caracteres de controle que indicariam arquivo binário
	for i := 0; i < n; i++ {
		if buffer[i] == 0 {
			return false
		}
	}

	// Se chegou aqui, é provavelmente um arquivo de texto
	return true
}

// ProcessDirectoryForAssistant processa um diretório para o Assistente da OpenAI
func (c *OpenAIAssistantClient) ProcessDirectoryForAssistant(ctx context.Context, dirPath string) ([]string, string, error) {
	dirPath, err := utils.ExpandPath(dirPath)
	if err != nil {
		return nil, "", fmt.Errorf("erro ao expandir o caminho: %w", err)
	}

	// Verificar se é um arquivo ou diretório
	fileInfo, err := os.Stat(dirPath)
	if err != nil {
		return nil, "", fmt.Errorf("erro ao acessar o caminho: %w", err)
	}

	var fileIDs []string
	var processedFiles int
	var totalSize int64
	var summary strings.Builder

	// Se for um arquivo único
	if !fileInfo.IsDir() {
		summary.WriteString(fmt.Sprintf("⏳ Processando arquivo único: %s (%.2f KB)\n",
			filepath.Base(dirPath), float64(fileInfo.Size())/1024))

		fileID, err := c.uploadFile(ctx, dirPath)
		if err != nil {
			return nil, "", err
		}

		if err := c.attachFileToAssistant(ctx, fileID); err != nil {
			return nil, "", fmt.Errorf("erro ao anexar arquivo ao assistente: %w", err)
		}

		fileIDs = append(fileIDs, fileID)
		summary.WriteString(fmt.Sprintf("✅ Arquivo carregado: %s (%.2f KB)\n",
			filepath.Base(dirPath), float64(fileInfo.Size())/1024))

		return fileIDs, summary.String(), nil
	}

	// Se for um diretório, processar recursivamente
	summary.WriteString(fmt.Sprintf("📂 Analisando diretório: %s\n", dirPath))

	// Configurar as opções de escaneamento
	scanOptions := utils.DefaultDirectoryScanOptions(c.logger)
	scanOptions.OnFileProcessed = func(info utils.FileInfo) {
		c.logger.Debug("Escaneando arquivo", zap.String("path", info.Path))
	}

	// Coletar arquivos do diretório
	files, err := utils.ProcessDirectory(dirPath, scanOptions)
	if err != nil {
		return nil, "", err
	}

	if len(files) == 0 {
		return nil, "", fmt.Errorf("nenhum arquivo relevante encontrado em '%s'", dirPath)
	}

	summary.WriteString(fmt.Sprintf("🔍 Encontrados %d arquivos para processamento\n", len(files)))

	// Ordenar arquivos por relevância (usando extensão como proxy de relevância)
	files = prioritizeFilesByType(files)

	// Verificar limite de arquivos para API Assistants
	if len(files) > MaxAssistantFiles {
		summary.WriteString(fmt.Sprintf("⚠️ AVISO: Encontrados %d arquivos, mas o limite é %d. "+
			"Serão carregados apenas os %d arquivos mais relevantes.\n\n",
			len(files), MaxAssistantFiles, MaxAssistantFiles))

		files = files[:MaxAssistantFiles]
	}

	// Calcular tamanho total para exibir progresso
	for _, file := range files {
		totalSize += file.Size
	}

	summary.WriteString(fmt.Sprintf("📊 Tamanho total a processar: %.2f MB\n",
		float64(totalSize)/1024/1024))

	// Exibir informação de início de processamento
	summary.WriteString("\n🔄 Iniciando processamento dos arquivos...\n\n")

	// Fazer upload de cada arquivo e anexar ao assistente
	var uploadedSize int64
	for i, file := range files {
		progressPercent := float64(uploadedSize) / float64(totalSize) * 100
		summary.WriteString(fmt.Sprintf("⏳ [%.1f%%] Processando %d/%d: %s\n",
			progressPercent, i+1, len(files), filepath.Base(file.Path)))

		fileID, err := c.uploadFile(ctx, file.Path)
		if err != nil {
			c.logger.Warn("Erro ao carregar arquivo",
				zap.String("path", file.Path),
				zap.Error(err))

			summary.WriteString(fmt.Sprintf("❌ Falha: %s - %s\n",
				filepath.Base(file.Path), err.Error()))
			continue
		}

		if err := c.attachFileToAssistant(ctx, fileID); err != nil {
			c.logger.Warn("Erro ao anexar arquivo ao assistente",
				zap.String("fileID", fileID),
				zap.Error(err))

			summary.WriteString(fmt.Sprintf("❌ Falha ao anexar: %s - %s\n",
				filepath.Base(file.Path), err.Error()))
			continue
		}

		fileIDs = append(fileIDs, fileID)
		processedFiles++
		uploadedSize += file.Size

		summary.WriteString(fmt.Sprintf("✅ Anexado: %s (%.2f KB)\n",
			filepath.Base(file.Path), float64(file.Size)/1024))
	}

	// Resumo final
	summary.WriteString(fmt.Sprintf("\n📊 RESUMO FINAL\n"+
		"===============================\n"+
		"▶️ %d/%d arquivos carregados com sucesso\n"+
		"▶️ %.2f MB processados\n"+
		"▶️ Os arquivos estão disponíveis para consulta\n",
		processedFiles, len(files), float64(uploadedSize)/1024/1024))

	if processedFiles == 0 {
		return nil, "", fmt.Errorf("nenhum arquivo pôde ser processado")
	}

	return fileIDs, summary.String(), nil
}

// prioritizeFilesByType ordena arquivos por relevância com base em extensões
func prioritizeFilesByType(files []utils.FileInfo) []utils.FileInfo {
	// Mapear extensões para pontuações de prioridade
	extensionPriority := map[string]int{
		// Código-fonte (maior prioridade)
		".go":    10,
		".java":  10,
		".py":    10,
		".tf":    9,
		".js":    9,
		".ts":    9,
		".jsx":   9,
		".tsx":   9,
		".php":   9,
		".rb":    9,
		".c":     9,
		".cpp":   9,
		".h":     9,
		".cs":    9,
		".swift": 9,
		".kt":    9,

		// Arquivos de configuração
		".json": 8,
		".yaml": 8,
		".yml":  8,
		".xml":  8,
		".toml": 8,
		".ini":  8,
		".env":  8,

		// Documentação
		".md":  7,
		".txt": 7,
		".rst": 7,

		// Web
		".html": 6,
		".css":  6,
		".scss": 6,

		// Arquivos binários ou de menor interesse
		".png": 1,
		".jpg": 1,
		".pdf": 1,
		".zip": 0,
		".jar": 0,
	}

	// Função para obter prioridade de extensão
	getPriority := func(file utils.FileInfo) int {
		ext := strings.ToLower(filepath.Ext(file.Path))
		if priority, ok := extensionPriority[ext]; ok {
			return priority
		}
		return 3 // Prioridade média para extensões desconhecidas
	}

	// Ordenar arquivos com base na prioridade
	sort.SliceStable(files, func(i, j int) bool {
		// Maior prioridade vem primeiro
		return getPriority(files[i]) > getPriority(files[j])
	})

	return files
}
