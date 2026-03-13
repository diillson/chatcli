/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package ctxmgr

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/utils"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// Manager gerencia contextos de forma thread-safe
type Manager struct {
	contexts         map[string]*FileContext      // ID -> FileContext
	attachedContexts map[string][]AttachedContext // SessionID -> AttachedContexts
	Storage          *Storage
	validator        *Validator
	processor        *Processor
	logger           *zap.Logger
	mu               sync.RWMutex
}

// NewManager cria uma nova instância do gerenciador de contextos
func NewManager(logger *zap.Logger) (*Manager, error) {
	storage, err := NewStorage(logger)
	if err != nil {
		return nil, fmt.Errorf("erro ao inicializar storage: %w", err)
	}

	manager := &Manager{
		contexts:         make(map[string]*FileContext),
		attachedContexts: make(map[string][]AttachedContext),
		Storage:          storage,
		validator:        NewValidator(logger),
		processor:        NewProcessor(logger),
		logger:           logger,
	}

	// Carregar contextos existentes do disco
	if err := manager.loadContexts(); err != nil {
		logger.Warn("Erro ao carregar contextos do disco", zap.Error(err))
	}

	return manager, nil
}

// CreateContext cria um novo contexto a partir de caminhos de arquivos/diretórios
func (m *Manager) CreateContext(name, description string, paths []string, mode ProcessingMode, tags []string, force bool) (*FileContext, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Validar nome
	if err := m.validator.ValidateName(name); err != nil {
		return nil, err
	}

	// Verificar se já existe
	if m.contextExistsByName(name) {
		if !force {
			return nil, fmt.Errorf("já existe um contexto com o nome '%s'. Use --force caso queira sobrescrever", name)
		}

		// Se force=true, deletar o existente primeiro
		for id, ctx := range m.contexts {
			if ctx.Name == name {
				if err := m.Storage.DeleteContext(id); err != nil {
					return nil, fmt.Errorf("erro ao remover contexto existente: %w", err)
				}
				delete(m.contexts, id)
				break
			}
		}
	}

	// Processar arquivos baseado no modo
	files, scanOpts, err := m.processor.ProcessPaths(paths, mode)
	if err != nil {
		return nil, fmt.Errorf("erro ao processar arquivos: %w", err)
	}

	// Validar tamanho total
	var totalSize int64
	for _, f := range files {
		totalSize += f.Size
	}

	if err := m.validator.ValidateTotalSize(totalSize); err != nil {
		return nil, err
	}

	// Criar contexto
	ctx := &FileContext{
		ID:          uuid.New().String(),
		Name:        name,
		Description: description,
		Files:       files,
		Mode:        mode,
		TotalSize:   totalSize,
		FileCount:   len(files),
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
		Tags:        tags,
		Metadata:    make(map[string]string),
		ScanOptions: scanOpts,
		ScanOptionsMetadata: ScanOptionsMetadata{
			MaxTotalSize:      scanOpts.MaxTotalSize,
			MaxFilesToProcess: scanOpts.MaxFilesToProcess,
			Extensions:        scanOpts.Extensions,
			ExcludeDirs:       scanOpts.ExcludeDirs,
			ExcludePatterns:   scanOpts.ExcludePatterns,
			IncludeHidden:     scanOpts.IncludeHidden,
		},
	}

	// NOVO: Se modo chunked, dividir em chunks
	if mode == ModeChunked {
		m.logger.Info("Dividindo arquivos em chunks",
			zap.String("context_name", name),
			zap.Int("total_files", len(files)))

		chunker := NewChunker(m.logger)
		chunks, err := chunker.DivideIntoChunks(files, ChunkSmart)
		if err != nil {
			return nil, fmt.Errorf("erro ao dividir em chunks: %w", err)
		}

		ctx.Chunks = chunks
		ctx.IsChunked = true
		ctx.ChunkStrategy = string(ChunkSmart)

		m.logger.Info("Contexto dividido em chunks",
			zap.String("context_id", ctx.ID),
			zap.Int("total_chunks", len(chunks)))
	}

	// Armazenar em memória
	m.contexts[ctx.ID] = ctx

	// Persistir no disco
	if err := m.Storage.SaveContext(ctx); err != nil {
		delete(m.contexts, ctx.ID)
		return nil, fmt.Errorf("erro ao salvar contexto: %w", err)
	}

	m.logger.Info("Contexto criado com sucesso",
		zap.String("id", ctx.ID),
		zap.String("name", ctx.Name),
		zap.Int("file_count", ctx.FileCount),
		zap.Int64("total_size", ctx.TotalSize),
		zap.Bool("is_chunked", ctx.IsChunked))

	return ctx, nil
}

// AttachContext anexa um contexto a uma sessão (não envia à LLM ainda)
func (m *Manager) AttachContext(sessionID, contextID string, priority int) error {
	opts := AttachOptions{Priority: priority}
	return m.AttachContextWithOptions(sessionID, contextID, opts)
}

// DetachContext remove um contexto anexado de uma sessão
func (m *Manager) DetachContext(sessionID, contextID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	attached := m.attachedContexts[sessionID]
	if len(attached) == 0 {
		return fmt.Errorf("nenhum contexto anexado a esta sessão")
	}

	// Encontrar e remover
	newAttached := make([]AttachedContext, 0, len(attached)-1)
	found := false
	var contextName string

	for _, a := range attached {
		if a.ContextID != contextID {
			newAttached = append(newAttached, a)
		} else {
			found = true
			if ctx, exists := m.contexts[contextID]; exists {
				contextName = ctx.Name
			}
		}
	}

	if !found {
		return fmt.Errorf("contexto não está anexado a esta sessão")
	}

	m.attachedContexts[sessionID] = newAttached

	m.logger.Info("Contexto desanexado da sessão",
		zap.String("session_id", sessionID),
		zap.String("context_id", contextID),
		zap.String("context_name", contextName))

	return nil
}

// DeleteContext remove um contexto permanentemente
func (m *Manager) DeleteContext(contextID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	ctx, exists := m.contexts[contextID]
	if !exists {
		return fmt.Errorf("contexto '%s' não encontrado", contextID)
	}

	// Verificar se está anexado a alguma sessão
	for sessionID, attached := range m.attachedContexts {
		for _, a := range attached {
			if a.ContextID == contextID {
				return fmt.Errorf("contexto '%s' está anexado à sessão '%s'. Desanexe antes de deletar", ctx.Name, sessionID)
			}
		}
	}

	// Deletar do disco
	if err := m.Storage.DeleteContext(contextID); err != nil {
		return fmt.Errorf("erro ao deletar contexto do disco: %w", err)
	}

	// Deletar da memória
	delete(m.contexts, contextID)

	m.logger.Info("Contexto deletado",
		zap.String("id", contextID),
		zap.String("name", ctx.Name))

	return nil
}

// MergeContexts mescla múltiplos contextos em um novo
func (m *Manager) MergeContexts(name, description string, contextIDs []string, opts MergeOptions) (*FileContext, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if len(contextIDs) < 2 {
		return nil, fmt.Errorf("é necessário pelo menos 2 contextos para mesclar")
	}

	// Validar nome do novo contexto
	if err := m.validator.ValidateName(name); err != nil {
		return nil, err
	}

	if m.contextExistsByName(name) {
		return nil, fmt.Errorf("já existe um contexto com o nome '%s'", name)
	}

	// Coletar todos os arquivos
	allFiles := make([]utils.FileInfo, 0)
	seenPaths := make(map[string]utils.FileInfo)

	for _, ctxID := range contextIDs {
		ctx, exists := m.contexts[ctxID]
		if !exists {
			return nil, fmt.Errorf("contexto '%s' não encontrado", ctxID)
		}

		for _, file := range ctx.Files {
			if opts.RemoveDuplicates {
				if existing, seen := seenPaths[file.Path]; seen {
					// Preferir versão mais recente
					if opts.PreferNewer {
						// Comparar tamanho como heurística
						if file.Size > existing.Size {
							seenPaths[file.Path] = file
						}
					}
					continue
				}
				seenPaths[file.Path] = file
			} else {
				allFiles = append(allFiles, file)
			}
		}
	}

	// Aplicar opções pós-processamento
	if opts.RemoveDuplicates {
		allFiles = make([]utils.FileInfo, 0, len(seenPaths))
		for _, file := range seenPaths {
			allFiles = append(allFiles, file)
		}
	}

	if opts.SortByPath {
		sort.Slice(allFiles, func(i, j int) bool {
			return allFiles[i].Path < allFiles[j].Path
		})
	}

	// Calcular tamanho total
	var totalSize int64
	for _, f := range allFiles {
		totalSize += f.Size
	}

	// Validar tamanho
	if err := m.validator.ValidateTotalSize(totalSize); err != nil {
		return nil, err
	}

	// Criar novo contexto mesclado
	mergedCtx := &FileContext{
		ID:          uuid.New().String(),
		Name:        name,
		Description: description,
		Files:       allFiles,
		Mode:        ModeFull, // Contextos mesclados sempre em modo full
		TotalSize:   totalSize,
		FileCount:   len(allFiles),
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
		Tags:        opts.Tags,
		Metadata: map[string]string{
			"merged_from": fmt.Sprintf("%d contexts", len(contextIDs)),
		},
		ScanOptions: utils.DirectoryScanOptions{}, // Vazio para contextos mesclados
	}

	// Armazenar
	m.contexts[mergedCtx.ID] = mergedCtx
	if err := m.Storage.SaveContext(mergedCtx); err != nil {
		delete(m.contexts, mergedCtx.ID)
		return nil, fmt.Errorf("erro ao salvar contexto mesclado: %w", err)
	}

	m.logger.Info("Contextos mesclados com sucesso",
		zap.String("new_context_id", mergedCtx.ID),
		zap.String("new_context_name", mergedCtx.Name),
		zap.Int("source_contexts", len(contextIDs)),
		zap.Int("total_files", mergedCtx.FileCount))

	return mergedCtx, nil
}

// GetContext retorna um contexto pelo ID
func (m *Manager) GetContext(contextID string) (*FileContext, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	ctx, exists := m.contexts[contextID]
	if !exists {
		return nil, fmt.Errorf("contexto '%s' não encontrado", contextID)
	}

	return ctx, nil
}

// GetContextByName retorna um contexto pelo nome
func (m *Manager) GetContextByName(name string) (*FileContext, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, ctx := range m.contexts {
		if ctx.Name == name {
			return ctx, nil
		}
	}

	return nil, fmt.Errorf("contexto com nome '%s' não encontrado", name)
}

// ListContexts lista todos os contextos com filtro opcional
func (m *Manager) ListContexts(filter *ContextFilter) ([]*FileContext, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]*FileContext, 0)

	for _, ctx := range m.contexts {
		if filter != nil {
			if !m.matchesFilter(ctx, filter) {
				continue
			}
		}
		result = append(result, ctx)
	}

	// Ordenar por data de criação (mais recente primeiro)
	sort.Slice(result, func(i, j int) bool {
		return result[i].CreatedAt.After(result[j].CreatedAt)
	})

	return result, nil
}

// GetAttachedContexts retorna os contextos anexados a uma sessão
func (m *Manager) GetAttachedContexts(sessionID string) ([]*FileContext, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	attached := m.attachedContexts[sessionID]
	result := make([]*FileContext, 0, len(attached))

	for _, a := range attached {
		if ctx, exists := m.contexts[a.ContextID]; exists {
			result = append(result, ctx)
		}
	}

	return result, nil
}

// UpdateContext atualiza um contexto existente
func (m *Manager) UpdateContext(name string, newPaths []string, newMode ProcessingMode, newTags []string, newDescription string) (*FileContext, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Buscar contexto existente
	var existingCtx *FileContext
	for _, ctx := range m.contexts {
		if ctx.Name == name {
			existingCtx = ctx
			break
		}
	}

	if existingCtx == nil {
		return nil, fmt.Errorf("contexto '%s' não encontrado", name)
	}

	// Processar novos arquivos se paths foram fornecidos
	var files []utils.FileInfo
	var scanOpts utils.DirectoryScanOptions
	var totalSize int64

	if len(newPaths) > 0 {
		mode := newMode
		if mode == "" {
			mode = existingCtx.Mode // Manter modo anterior se não especificado
		}

		var err error
		files, scanOpts, err = m.processor.ProcessPaths(newPaths, mode)
		if err != nil {
			return nil, fmt.Errorf("erro ao processar arquivos: %w", err)
		}

		for _, f := range files {
			totalSize += f.Size
		}

		if err := m.validator.ValidateTotalSize(totalSize); err != nil {
			return nil, err
		}

		existingCtx.Files = files
		existingCtx.Mode = mode
		existingCtx.TotalSize = totalSize
		existingCtx.FileCount = len(files)
		existingCtx.ScanOptions = scanOpts
		existingCtx.ScanOptionsMetadata = ScanOptionsMetadata{
			MaxTotalSize:      scanOpts.MaxTotalSize,
			MaxFilesToProcess: scanOpts.MaxFilesToProcess,
			Extensions:        scanOpts.Extensions,
			ExcludeDirs:       scanOpts.ExcludeDirs,
			ExcludePatterns:   scanOpts.ExcludePatterns,
			IncludeHidden:     scanOpts.IncludeHidden,
		}
	}

	// Atualizar descrição se fornecida
	if newDescription != "" {
		existingCtx.Description = newDescription
	}

	// Atualizar tags se fornecidas
	if len(newTags) > 0 {
		existingCtx.Tags = newTags
	}

	// IMPORTANTE: Atualizar timestamp
	existingCtx.UpdatedAt = time.Now()

	// Re-dividir em chunks se necessário
	if existingCtx.Mode == ModeChunked && len(files) > 0 {
		m.logger.Info("Re-dividindo arquivos em chunks após atualização",
			zap.String("context_name", name),
			zap.Int("total_files", len(files)))

		chunker := NewChunker(m.logger)
		chunks, err := chunker.DivideIntoChunks(files, ChunkSmart)
		if err != nil {
			return nil, fmt.Errorf("erro ao dividir em chunks: %w", err)
		}

		existingCtx.Chunks = chunks
		existingCtx.IsChunked = true
		existingCtx.ChunkStrategy = string(ChunkSmart)
	}

	// Salvar no disco
	if err := m.Storage.SaveContext(existingCtx); err != nil {
		return nil, fmt.Errorf("erro ao salvar contexto atualizado: %w", err)
	}

	m.logger.Info("Contexto atualizado com sucesso",
		zap.String("id", existingCtx.ID),
		zap.String("name", existingCtx.Name),
		zap.Int("file_count", existingCtx.FileCount),
		zap.Int64("total_size", existingCtx.TotalSize))

	return existingCtx, nil
}

// CORREÇÃO 2: Refatorada para usar a estrutura de dados correta e lidar com chunks selecionados.
// BuildPromptMessages agora considera chunks selecionados
func (m *Manager) BuildPromptMessages(sessionID string, opts FormatOptions) ([]models.Message, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	attachments := m.attachedContexts[sessionID]
	if len(attachments) == 0 {
		return nil, nil
	}

	// Ordenar por prioridade (menor primeiro)
	sort.Slice(attachments, func(i, j int) bool {
		return attachments[i].Priority < attachments[j].Priority
	})

	var messages []models.Message

	for _, attachment := range attachments {
		ctx, exists := m.contexts[attachment.ContextID]
		if !exists {
			m.logger.Warn("Contexto anexado não encontrado durante a construção do prompt",
				zap.String("contextID", attachment.ContextID))
			continue
		}

		var content string
		// Se tem chunks selecionados, usar apenas eles
		if len(attachment.SelectedChunks) > 0 {
			content = fmt.Sprintf("📦 CONTEXTO: %s (Chunks: %v)\n", ctx.Name, attachment.SelectedChunks)
			if opts.IncludeMetadata {
				content += fmt.Sprintf("Modo: %s | Chunks Selecionados: %d de %d\n\n",
					ctx.Mode, len(attachment.SelectedChunks), len(ctx.Chunks))
			}

			// Incluir apenas chunks selecionados
			for _, chunkNum := range attachment.SelectedChunks {
				if chunkNum < 1 || chunkNum > len(ctx.Chunks) {
					continue // Ignora chunks inválidos
				}
				chunk := ctx.Chunks[chunkNum-1] // Índice é 0-based
				content += m.formatChunk(chunk, opts)
			}
		} else {
			// Usar formatação normal (todos os arquivos ou todos os chunks)
			content = m.formatContextContent(ctx, opts)
		}

		role := strings.TrimSpace(strings.ToLower(opts.Role))
		if role == "" {
			role = "user" // default seguro: contexto não deve competir com system prompt
		}
		if role != "user" && role != "system" {
			role = "user"
		}

		messages = append(messages, models.Message{
			Role:    role,
			Content: content,
		})
	}

	return messages, nil
}

// GetMetrics retorna métricas sobre os contextos
func (m *Manager) GetMetrics() *ContextMetrics {
	m.mu.RLock()
	defer m.mu.RUnlock()

	metrics := &ContextMetrics{
		TotalContexts:    len(m.contexts),
		AttachedContexts: 0,
		TotalFiles:       0,
		TotalSizeBytes:   0,
		ContextsByMode:   make(map[string]int),
		LastUpdated:      time.Now(),
		StoragePath:      m.Storage.basePath,
	}

	for _, ctx := range m.contexts {
		metrics.TotalFiles += ctx.FileCount
		metrics.TotalSizeBytes += ctx.TotalSize
		metrics.ContextsByMode[string(ctx.Mode)]++
	}

	// Contar contextos anexados (unique)
	uniqueAttached := make(map[string]bool)
	for _, attached := range m.attachedContexts {
		for _, a := range attached {
			uniqueAttached[a.ContextID] = true
		}
	}
	metrics.AttachedContexts = len(uniqueAttached)

	return metrics
}

// GetSessionsForContext returns all session IDs that have the given context attached.
func (m *Manager) GetSessionsForContext(contextID string) []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var sessions []string
	for sessionID, attached := range m.attachedContexts {
		for _, a := range attached {
			if a.ContextID == contextID {
				sessions = append(sessions, sessionID)
				break
			}
		}
	}

	sort.Strings(sessions)
	return sessions
}

// Helper methods

func (m *Manager) contextExistsByName(name string) bool {
	for _, ctx := range m.contexts {
		if ctx.Name == name {
			return true
		}
	}
	return false
}

func (m *Manager) matchesFilter(ctx *FileContext, filter *ContextFilter) bool {
	// Filtrar por tags
	if len(filter.Tags) > 0 {
		hasTag := false
		for _, filterTag := range filter.Tags {
			for _, ctxTag := range ctx.Tags {
				if ctxTag == filterTag {
					hasTag = true
					break
				}
			}
			if hasTag {
				break
			}
		}
		if !hasTag {
			return false
		}
	}

	// Filtrar por modo
	if filter.Mode != "" && ctx.Mode != filter.Mode {
		return false
	}

	// Filtrar por tamanho
	if filter.MinSize > 0 && ctx.TotalSize < filter.MinSize {
		return false
	}
	if filter.MaxSize > 0 && ctx.TotalSize > filter.MaxSize {
		return false
	}

	// Filtrar por data
	if filter.CreatedAfter != nil && ctx.CreatedAt.Before(*filter.CreatedAfter) {
		return false
	}
	if filter.CreatedBefore != nil && ctx.CreatedAt.After(*filter.CreatedBefore) {
		return false
	}

	// Filtrar por padrão de nome
	if filter.NamePattern != "" {
		matched, err := regexp.MatchString(filter.NamePattern, ctx.Name)
		if err != nil || !matched {
			return false
		}
	}

	return true
}

func (m *Manager) formatContextContent(ctx *FileContext, opts FormatOptions) string {
	var builder strings.Builder

	// Cabeçalho do contexto
	if opts.IncludeMetadata {
		builder.WriteString(fmt.Sprintf("📦 CONTEXT: %s\n", ctx.Name))
		if ctx.Description != "" {
			builder.WriteString(fmt.Sprintf("Description: %s\n", ctx.Description))
		}
		if opts.IncludeTimestamp {
			builder.WriteString(fmt.Sprintf("Created: %s\n", ctx.CreatedAt.Format(time.RFC3339)))
		}
		builder.WriteString(fmt.Sprintf("Mode: %s | Files: %d | Size: %.2f MB\n",
			ctx.Mode, ctx.FileCount, float64(ctx.TotalSize)/1024/1024))
		if len(ctx.Tags) > 0 {
			builder.WriteString(fmt.Sprintf("Tags: %s\n", strings.Join(ctx.Tags, ", ")))
		}
		builder.WriteString("\n")
	}

	// Formatar arquivos usando a função já existente do utils
	formattedContent := utils.FormatDirectoryContent(ctx.Files, ctx.TotalSize)
	builder.WriteString(formattedContent)

	return builder.String()
}

func (m *Manager) loadContexts() error {
	contexts, err := m.Storage.LoadAllContexts()
	if err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	for _, ctx := range contexts {
		m.contexts[ctx.ID] = ctx
	}

	m.logger.Info("Contextos carregados do disco",
		zap.Int("count", len(contexts)))

	return nil
}

// CORREÇÃO 1: Função refatorada para usar a estrutura de dados correta do Manager.
// AttachContextWithOptions anexa contexto com opções avançadas
func (m *Manager) AttachContextWithOptions(sessionID, contextID string, opts AttachOptions) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	ctx, exists := m.contexts[contextID]
	if !exists {
		return fmt.Errorf("contexto '%s' não encontrado", contextID)
	}

	// Verificar se já está anexado
	for _, a := range m.attachedContexts[sessionID] {
		if a.ContextID == contextID {
			return fmt.Errorf("contexto '%s' já está anexado a esta sessão", ctx.Name)
		}
	}

	// Criar o anexo com todas as opções
	attachment := AttachedContext{
		ContextID:      contextID,
		AttachedAt:     time.Now(),
		Priority:       opts.Priority,
		SelectedChunks: opts.SelectedChunks,
	}

	// Adicionar à lista de anexos da sessão
	m.attachedContexts[sessionID] = append(m.attachedContexts[sessionID], attachment)

	// Ordenar por prioridade
	sort.Slice(m.attachedContexts[sessionID], func(i, j int) bool {
		return m.attachedContexts[sessionID][i].Priority < m.attachedContexts[sessionID][j].Priority
	})

	m.logger.Info("Contexto anexado à sessão com opções",
		zap.String("session_id", sessionID),
		zap.String("context_id", contextID),
		zap.String("context_name", ctx.Name),
		zap.Int("priority", opts.Priority),
		zap.Ints("selected_chunks", opts.SelectedChunks))

	return nil
}

// CORREÇÃO 3: Corrigido o tipo do parâmetro 'chunk' de 'Chunk' para 'FileChunk'.
// formatChunk formata um chunk individual
func (m *Manager) formatChunk(chunk FileChunk, opts FormatOptions) string {
	var b strings.Builder

	b.WriteString(fmt.Sprintf("\n📦 CHUNK %d/%d: %s\n",
		chunk.Index, chunk.TotalChunks, chunk.Description))
	b.WriteString(strings.Repeat("=", 80) + "\n\n")

	for _, file := range chunk.Files {
		b.WriteString(fmt.Sprintf("📄 ARQUIVO: %s\n", file.Path))
		if opts.IncludeMetadata {
			b.WriteString(fmt.Sprintf("Tipo: %s | Tamanho: %.2f KB\n",
				file.Type, float64(file.Size)/1024))
		}
		b.WriteString("```\n")
		b.WriteString(file.Content)
		b.WriteString("\n```\n\n")
	}

	return b.String()
}
