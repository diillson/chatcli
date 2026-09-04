/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package ctxmgr

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/diillson/chatcli/cli/workspace/threatscan"
	"github.com/diillson/chatcli/llm/embedding"
	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/utils"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// Manager gerencia contextos de forma thread-safe
type Manager struct {
	// retrievedBudgetChars bounds the retrieved passages per turn (0 = default).
	retrievedBudgetChars int
	// warming single-flights WarmContext per context id.
	warmMu           sync.Mutex
	warming          map[string]bool
	warmCtx          context.Context
	contexts         map[string]*FileContext      // ID -> FileContext
	attachedContexts map[string][]AttachedContext // SessionID -> AttachedContexts
	Storage          *Storage
	validator        *Validator
	processor        *Processor
	logger           *zap.Logger
	mu               sync.RWMutex

	// retrieval is the optional semantic-retrieval engine, wired post-construction
	// via AttachEmbeddingProvider. nil/disabled → --rag attachments degrade to the
	// legacy whole-content path, so retrieval is purely additive.
	retrieval *RetrievalEngine
	// reranker is the optional rerank stage (AttachReranker); it survives
	// engine rebuilds because AttachEmbeddingProvider re-applies it.
	reranker Reranker

	// digestMu/digestCache memoize the knowledge index card per context
	// revision: prompt assembly runs every turn and the digest walk is
	// O(corpus), so recomputing it per turn scales with the corpus for free.
	digestMu    sync.Mutex
	digestCache map[string]knowledgeDigestEntry
}

// AttachEmbeddingProvider wires (or rewires) the embedding provider that powers
// semantic /context retrieval. A Null/absent provider still yields a live
// engine: Enabled() stays false (so --rag attachments degrade to whole content
// exactly as before), while knowledge-mode hybrid retrieval keeps its keyless
// BM25 floor. Safe to call once at startup; provider-agnostic across backends.
// AttachReranker installs (or clears, with nil) the rerank stage applied to
// every hybrid retrieval. Purely additive: with nil the fused order stands.
func (m *Manager) AttachReranker(r Reranker) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reranker = r
	if m.retrieval != nil {
		m.retrieval.SetReranker(r)
	}
}

// AttachWeight returns the merge weight of an attached corpus (1.0 when
// unset or not attached).
func (m *Manager) AttachWeight(sessionID, contextID string) float64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, a := range m.attachedContexts[sessionID] {
		if a.ContextID == contextID && a.Weight > 0 {
			return a.Weight
		}
	}
	return 1.0
}

func (m *Manager) AttachEmbeddingProvider(provider embedding.Provider) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.retrieval = NewRetrievalEngine(provider, m.Storage.basePath, m.logger)
	if m.reranker != nil {
		m.retrieval.SetReranker(m.reranker)
	}
}

// RetrievalEnabled reports whether a real embedding provider backs retrieval.
func (m *Manager) RetrievalEnabled() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.retrieval.Enabled()
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

// NewManagerWithBasePath is NewManager over an explicit storage directory —
// for surfaces and tests that must not touch the user's ~/.chatcli.
func NewManagerWithBasePath(basePath string, logger *zap.Logger) (*Manager, error) {
	if err := os.MkdirAll(basePath, 0o700); err != nil {
		return nil, fmt.Errorf("erro ao inicializar storage: %w", err)
	}
	manager := &Manager{
		contexts:         make(map[string]*FileContext),
		attachedContexts: make(map[string][]AttachedContext),
		Storage:          &Storage{basePath: basePath, logger: logger},
		validator:        NewValidator(logger),
		processor:        NewProcessor(logger),
		logger:           logger,
	}
	if err := manager.loadContexts(); err != nil {
		logger.Warn("Erro ao carregar contextos do disco", zap.Error(err))
	}
	return manager, nil
}

// CreateContext cria um novo contexto a partir de caminhos de arquivos/diretórios
func (m *Manager) CreateContext(ctx context.Context, name, description string, paths []string, mode ProcessingMode, tags []string, force bool) (*FileContext, error) {
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

	// Processar arquivos baseado no modo. Em knowledge mode, arquivos .jsonl
	// são corpora docs-flatten e entram pelo parser nativo (um FileInfo por
	// chunk, preservando source/título/proveniência); demais caminhos seguem
	// pelo scanner normal, então diretórios de docs também viram knowledge.
	knowledgeMeta := map[string]string{segmenterMetaKey: segmenterV2}
	var files []utils.FileInfo
	var scanPaths []string
	if mode == ModeKnowledge {
		for _, p := range paths {
			if !isJSONLPath(p) {
				scanPaths = append(scanPaths, p)
				continue
			}
			expanded, expandErr := utils.ExpandPath(p)
			if expandErr != nil {
				expanded = p
			}
			kfiles, kmeta, ingestErr := ingestKnowledgeJSONL(expanded, m.logger)
			if ingestErr != nil {
				return nil, ingestErr
			}
			files = append(files, kfiles...)
			for k, v := range kmeta {
				knowledgeMeta[k] = v
			}
		}
	} else {
		scanPaths = paths
	}

	scanOpts := utils.DefaultDirectoryScanOptions(m.logger)
	if len(scanPaths) > 0 {
		scanned, opts, err := m.processor.ProcessPaths(ctx, scanPaths, mode)
		if err != nil {
			return nil, fmt.Errorf("erro ao processar arquivos: %w", err)
		}
		files = append(files, scanned...)
		scanOpts = opts
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
	fctx := &FileContext{
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
		Metadata:    knowledgeMeta,
		ScanOptions: scanOpts,
		ScanOptionsMetadata: ScanOptionsMetadata{
			MaxTotalSize:      scanOpts.MaxTotalSize,
			MaxFilesToProcess: scanOpts.MaxFilesToProcess,
			Extensions:        scanOpts.Extensions,
			ExcludeDirs:       scanOpts.ExcludeDirs,
			ExcludePatterns:   scanOpts.ExcludePatterns,
			IncludeHidden:     scanOpts.IncludeHidden,
		},
		SourcePaths: expandSourcePaths(paths),
		FileStamps:  stampFiles(files),
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

		fctx.Chunks = chunks
		fctx.IsChunked = true
		fctx.ChunkStrategy = string(ChunkSmart)

		m.logger.Info("Contexto dividido em chunks",
			zap.String("context_id", fctx.ID),
			zap.Int("total_chunks", len(chunks)))
	}

	// Armazenar em memória
	m.contexts[fctx.ID] = fctx

	// Persistir no disco
	if err := m.Storage.SaveContext(fctx); err != nil {
		delete(m.contexts, fctx.ID)
		return nil, fmt.Errorf("erro ao salvar contexto: %w", err)
	}

	m.logger.Info("Contexto criado com sucesso",
		zap.String("id", fctx.ID),
		zap.String("name", fctx.Name),
		zap.Int("file_count", fctx.FileCount),
		zap.Int64("total_size", fctx.TotalSize),
		zap.Bool("is_chunked", fctx.IsChunked))

	return fctx, nil
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

	// Deletar da memória + caches derivados (lexical/vetorial em RAM, vetores
	// persistidos em disco e o digest memoizado) — nada órfão sobrevive.
	delete(m.contexts, contextID)
	m.retrieval.DropCache(contextID)
	m.digestMu.Lock()
	delete(m.digestCache, contextID)
	m.digestMu.Unlock()

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

// RenderContext renders one context's content by name for read-only export
// (MCP resources). Knowledge contexts render their index card — the corpus
// itself is read per document via KnowledgeTOCByName/KnowledgeDocumentByName.
func (m *Manager) RenderContext(name string) (string, error) {
	fc, err := m.GetContextByName(name)
	if err != nil {
		return "", err
	}
	if fc.Mode == ModeKnowledge {
		return m.KnowledgeDigest(fc), nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.formatContextContent(fc, FormatOptions{IncludeMetadata: true, Role: "system"}), nil
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
// AttachedRecords returns copies of the attachment records (context id,
// priority, selected chunks, retrieval mode) bound to sessionID — what a
// saved session needs to re-attach the same contexts on load. Attach state
// is otherwise process-local and was lost on every restart.
func (m *Manager) AttachedRecords(sessionID string) []AttachedContext {
	m.mu.RLock()
	defer m.mu.RUnlock()
	src := m.attachedContexts[sessionID]
	if len(src) == 0 {
		return nil
	}
	out := make([]AttachedContext, len(src))
	for i, a := range src {
		out[i] = a
		if len(a.SelectedChunks) > 0 {
			out[i].SelectedChunks = append([]int(nil), a.SelectedChunks...)
		}
	}
	return out
}

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
func (m *Manager) UpdateContext(ctx context.Context, name string, newPaths []string, newMode ProcessingMode, newTags []string, newDescription string) (*FileContext, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Buscar contexto existente
	var existingCtx *FileContext
	for _, fc := range m.contexts {
		if fc.Name == name {
			existingCtx = fc
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

		// Mesmo gate de knowledge mode do CreateContext: corpora .jsonl do
		// docs-flatten entram pelo parser nativo (um FileInfo por chunk,
		// preservando source/título/proveniência), enquanto os demais caminhos
		// seguem pelo scanner normal. Sem isto, um .jsonl seria ingerido como um
		// único arquivo monolítico (units: 1) e a proveniência kb.* ficaria stale.
		knowledgeMeta := map[string]string{}
		var scanPaths []string
		if mode == ModeKnowledge {
			for _, p := range newPaths {
				if !isJSONLPath(p) {
					scanPaths = append(scanPaths, p)
					continue
				}
				expanded, expandErr := utils.ExpandPath(p)
				if expandErr != nil {
					expanded = p
				}
				kfiles, kmeta, ingestErr := ingestKnowledgeJSONL(expanded, m.logger)
				if ingestErr != nil {
					return nil, ingestErr
				}
				files = append(files, kfiles...)
				for k, v := range kmeta {
					knowledgeMeta[k] = v
				}
			}
		} else {
			scanPaths = newPaths
		}

		scanOpts = utils.DefaultDirectoryScanOptions(m.logger)
		if len(scanPaths) > 0 {
			scanned, opts, err := m.processor.ProcessPaths(ctx, scanPaths, mode)
			if err != nil {
				return nil, fmt.Errorf("erro ao processar arquivos: %w", err)
			}
			files = append(files, scanned...)
			scanOpts = opts
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
		existingCtx.SourcePaths = expandSourcePaths(newPaths)
		existingCtx.FileStamps = stampFiles(files)
		existingCtx.ScanOptions = scanOpts
		existingCtx.ScanOptionsMetadata = ScanOptionsMetadata{
			MaxTotalSize:      scanOpts.MaxTotalSize,
			MaxFilesToProcess: scanOpts.MaxFilesToProcess,
			Extensions:        scanOpts.Extensions,
			ExcludeDirs:       scanOpts.ExcludeDirs,
			ExcludePatterns:   scanOpts.ExcludePatterns,
			IncludeHidden:     scanOpts.IncludeHidden,
		}

		// Refrescar a proveniência kb.* a partir do novo corpus — commit e
		// contagem de sources mudam a cada re-ingestão. Merge (não overwrite)
		// preserva quaisquer outras chaves de metadata já gravadas no contexto.
		if len(knowledgeMeta) > 0 {
			if existingCtx.Metadata == nil {
				existingCtx.Metadata = map[string]string{}
			}
			for k, v := range knowledgeMeta {
				existingCtx.Metadata[k] = v
			}
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
	if existingCtx.Mode == ModeChunked {
		// Só re-chunka quando há arquivos novos; um update apenas de descrição/
		// tags preserva os chunks existentes (conteúdo inalterado).
		if len(files) > 0 {
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
	} else if len(files) > 0 {
		// O modo deixou de ser chunked nesta atualização (ex.: chunked→knowledge):
		// descartar o estado de chunk órfão para que um contexto knowledge/full
		// nunca carregue um índice de chunks fantasma (IsChunked stale é landmine
		// para qualquer caminho que ramifique em fc.IsChunked).
		existingCtx.Chunks = nil
		existingCtx.IsChunked = false
		existingCtx.ChunkStrategy = ""
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
	msgs, _, err := m.BuildPromptMessagesBudgeted(sessionID, opts, 0)
	return msgs, err
}

// foldedDigestBudget is the compact index card a knowledge base shrinks to
// when the prompt budget is tight.
const foldedDigestBudget = 900

// BuildPromptMessagesBudgeted is BuildPromptMessages under a character
// budget (0 = unbounded). When the running total would cross maxChars,
// knowledge digests shrink to their compact card and whole-content
// attachments fold into an index card naming the files and how to pull
// them. Returns the names of the contexts that folded.
func (m *Manager) BuildPromptMessagesBudgeted(sessionID string, opts FormatOptions, maxChars int) ([]models.Message, []string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	// Copy before sorting: this method holds only the READ lock, and sorting
	// the shared slice in place would mutate state other readers iterate.
	// Today the list arrives pre-sorted (attach keeps it ordered), so the
	// in-place sort happened to never swap — but that is an invariant of a
	// different method, not something this read path may lean on.
	attachments := append([]AttachedContext(nil), m.attachedContexts[sessionID]...)
	if len(attachments) == 0 {
		return nil, nil, nil
	}
	used := 0
	var folded []string
	over := func(n int) bool { return maxChars > 0 && used+n > maxChars }

	// Ordenar por prioridade (menor primeiro)
	sort.Slice(attachments, func(i, j int) bool {
		return attachments[i].Priority < attachments[j].Priority
	})

	messages := make([]models.Message, 0, len(attachments))

	for _, attachment := range attachments {
		ctx, exists := m.contexts[attachment.ContextID]
		if !exists {
			m.logger.Warn("Contexto anexado não encontrado durante a construção do prompt",
				zap.String("contextID", attachment.ContextID))
			continue
		}

		// Knowledge contexts inject only their index card here (stable, cheap,
		// cacheable); the corpus itself is reached per turn via the volatile
		// hybrid-retrieval block. This is what keeps a multi-MB corpus at a
		// fixed few-hundred-token cost.
		if ctx.Mode == ModeKnowledge {
			digest := m.KnowledgeDigest(ctx)
			if over(len(digest)) {
				digest = BuildKnowledgeDigest(ctx, foldedDigestBudget)
				folded = append(folded, ctx.Name)
			}
			used += len(digest)
			messages = append(messages, models.Message{
				Role:    promptRole(opts.Role),
				Content: digest,
			})
			continue
		}

		// Retrieval-mode attachments are query-driven and therefore volatile;
		// they are injected separately via BuildRetrievedContextMessages so they
		// never poison the cached prefix this method feeds. Only skip them when
		// retrieval is actually enabled — otherwise a --rag attachment with no
		// embedding provider must still appear here as whole content.
		if attachment.RetrievalTopK > 0 && m.retrieval.Enabled() {
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

		if over(len(content)) {
			content = foldedContextCard(ctx, maxChars-used)
			folded = append(folded, ctx.Name)
		}
		used += len(content)
		messages = append(messages, models.Message{
			Role:    promptRole(opts.Role),
			Content: content,
		})
	}

	return messages, folded, nil
}

// foldedContextCard is what a whole-content attachment becomes when the
// prompt budget cannot hold it: the file list (as far as budget allows)
// and the ways to pull the content on demand.
func foldedContextCard(fc *FileContext, budget int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "📦 CONTEXT: %s (folded: %d file(s), ~%s tokens exceed the prompt budget — content NOT inlined)\n", fc.Name, fc.FileCount, approxTokens(fc.TotalSize))
	b.WriteString("Pull it on demand: /context attach ")
	b.WriteString(fc.Name)
	b.WriteString(" --rag retrieves only the passages relevant to each turn; /context detach frees the slot.\n")
	b.WriteString("Files:\n")
	for _, f := range fc.Files {
		line := fmt.Sprintf("- %s (%d B)\n", f.Path, f.Size)
		if budget > 0 && b.Len()+len(line)+24 > budget {
			b.WriteString("- …\n")
			break
		}
		b.WriteString(line)
	}
	return b.String()
}

// promptRole normaliza o role das mensagens de contexto: default seguro "user"
// (contexto não deve competir com system prompt) e somente user|system passam.
func promptRole(role string) string {
	role = strings.TrimSpace(strings.ToLower(role))
	if role != "user" && role != "system" {
		return "user"
	}
	return role
}

// BuildRetrievedContextMessages runs per-turn retrieval for every attachment
// that is query-driven — knowledge contexts (always; hybrid BM25+vectors, no
// API key required) and --rag attachments (vector-only, needs a provider) —
// and returns one message per context holding only the passages relevant to
// query. Returns nil when nothing opted in or the query is empty, so the
// caller can skip the volatile block entirely.
//
// A failure on a single context is logged and skipped, never fatal: a flaky
// embedding call must not break the turn. The query embedding happens outside
// the manager lock because it does network I/O.
func (m *Manager) BuildRetrievedContextMessages(ctx context.Context, sessionID, query string) ([]models.Message, error) {
	m.mu.RLock()
	engine := m.retrieval
	attachments := append([]AttachedContext(nil), m.attachedContexts[sessionID]...)
	wanted := make(map[string]*FileContext)
	for _, a := range attachments {
		c, ok := m.contexts[a.ContextID]
		if !ok {
			continue
		}
		if c.Mode == ModeKnowledge || a.RetrievalTopK > 0 {
			wanted[a.ContextID] = c
		}
	}
	m.mu.RUnlock()

	if engine == nil || len(wanted) == 0 || strings.TrimSpace(query) == "" {
		return nil, nil
	}

	sort.Slice(attachments, func(i, j int) bool {
		return attachments[i].Priority < attachments[j].Priority
	})

	// One character budget for everything retrieved this turn, shared by
	// the attachments in priority order: top-K × attachments used to grow
	// without bound. Passages already emitted by a higher-priority
	// attachment (same id, or same content at the same place) are dropped.
	budget := m.retrievedBudget()
	used := 0
	seen := make(map[string]struct{}, 64)
	messages := make([]models.Message, 0, len(wanted))
	for _, a := range attachments {
		fc, ok := wanted[a.ContextID]
		if !ok {
			continue
		}
		if used >= budget {
			m.logger.Info("retrieved block budget exhausted; skipping attachment", zap.String("context", fc.Name), zap.Int("budget_chars", budget))
			break
		}
		var segs []Segment
		var err error
		knowledge := fc.Mode == ModeKnowledge
		if knowledge {
			segs, err = engine.RetrieveHybrid(ctx, fc, query, a.RetrievalTopK)
			if err != nil {
				m.logger.Warn("knowledge retrieval failed; skipping",
					zap.String("context", fc.Name), zap.Error(err))
				continue
			}
		} else {
			if !engine.Enabled() {
				continue // --rag sem provider: degradou para conteúdo inteiro no prefixo
			}
			segs, err = engine.Retrieve(ctx, fc, query, a.RetrievalTopK)
			if err != nil {
				m.logger.Warn("context semantic retrieval failed; skipping",
					zap.String("context", fc.Name), zap.Error(err))
				continue
			}
		}
		segs = dedupSegments(segs, seen)
		segs = fitSegments(segs, budget-used)
		if len(segs) == 0 {
			continue
		}
		var block string
		if knowledge {
			block = FormatKnowledgeSegmentsBlock(fc.Name, segs)
		} else {
			block = FormatSegmentsBlock(fc.Name, query, segs)
		}
		if block == "" {
			continue
		}
		// A corpus cloned from the web is untrusted text: the same threat
		// scan the workspace instruction files get, before it reaches the
		// model as retrieved data.
		if threatscan.Enabled() {
			if sanitized, blocked := threatscan.Sanitize(block, threatscan.ScopeContext); blocked > 0 {
				m.logger.Warn("retrieved passages carried instruction-like content; sanitized",
					zap.String("context", fc.Name), zap.Int("blocked", blocked))
				block = sanitized
			}
		}
		used += len(block)
		messages = append(messages, models.Message{Role: "system", Content: block})
	}
	return messages, nil
}

// warmAfterAttach starts the embedding warm-up for attachments that will
// be retrieved per turn (knowledge contexts and --rag attachments).
func (m *Manager) warmAfterAttach(contextID string, opts AttachOptions) {
	m.mu.RLock()
	fc := m.contexts[contextID]
	m.mu.RUnlock()
	if fc == nil || (fc.Mode != ModeKnowledge && opts.RetrievalTopK <= 0) {
		return
	}
	m.WarmContext(contextID)
}

// warmBase is the root context of background warm-ups started from
// callers that carry no context of their own (attach is synchronous and
// context-free by API); the warm-up is bounded by the manager's lifetime,
// not by any request.
var warmBase = context.Background()

// DefaultRetrievedBudgetChars bounds the retrieved passages of one turn
// across every attachment (~6K tokens): enough for eight full passages,
// small next to any model window.
const DefaultRetrievedBudgetChars = 24_000

// SetRetrievedBudget overrides the per-turn retrieved-passages budget
// (chars); <= 0 restores the default.
func (m *Manager) SetRetrievedBudget(chars int) {
	m.mu.Lock()
	m.retrievedBudgetChars = chars
	m.mu.Unlock()
}

func (m *Manager) retrievedBudget() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.retrievedBudgetChars > 0 {
		return m.retrievedBudgetChars
	}
	return DefaultRetrievedBudgetChars
}

// dedupSegments drops passages already emitted this turn (by id, or by
// content at the same file position) and records the survivors.
func dedupSegments(segs []Segment, seen map[string]struct{}) []Segment {
	out := segs[:0:0]
	for _, s := range segs {
		keys := []string{s.ID, s.FilePath + "\x00" + itoa(s.StartLine) + "\x00" + s.Content}
		dup := false
		for _, k := range keys {
			if _, ok := seen[k]; ok {
				dup = true
				break
			}
		}
		if dup {
			continue
		}
		for _, k := range keys {
			seen[k] = struct{}{}
		}
		out = append(out, s)
	}
	return out
}

// fitSegments keeps the leading passages that fit in remaining chars
// (rendering overhead included); the first passage always fits when the
// budget is not already exhausted, truncated to the remaining room.
func fitSegments(segs []Segment, remaining int) []Segment {
	if remaining <= 0 {
		return nil
	}
	const overhead = 96 // header line + fences per passage
	out := make([]Segment, 0, len(segs))
	for _, s := range segs {
		cost := len(s.Content) + overhead
		if cost > remaining {
			if len(out) == 0 && remaining > overhead+200 {
				// Rune-safe cut, and the citation range shrinks with it so
				// the line numbers in the header stay true.
				cut := alignRuneBefore(s.Content, remaining-overhead-1)
				s.Content = s.Content[:cut] + "…"
				s.EndLine = s.StartLine + strings.Count(s.Content, "\n")
				out = append(out, s)
			}
			break
		}
		remaining -= cost
		out = append(out, s)
	}
	return out
}

// WarmContext embeds the passages of a context that the vector index does
// not hold yet, in the background (single-flight per context). It is what
// attach and refresh call so the first query after them does not pay the
// corpus embedding in the turn. The work is bounded by the manager's
// lifetime, not by any request. No-op without a provider.
func (m *Manager) WarmContext(contextID string) {
	m.warmMu.Lock()
	if m.warmCtx == nil {
		m.warmCtx = warmBase
	}
	ctx := m.warmCtx
	m.warmMu.Unlock()
	m.warmContextCtx(ctx, contextID)
}

// warmContextCtx is WarmContext bounded by ctx (refresh passes a
// cancellation-free derivative of its own).
func (m *Manager) warmContextCtx(ctx context.Context, contextID string) {
	m.mu.RLock()
	engine := m.retrieval
	fc := m.contexts[contextID]
	m.mu.RUnlock()
	if engine == nil || !engine.Enabled() || fc == nil {
		return
	}
	m.warmMu.Lock()
	if m.warming == nil {
		m.warming = map[string]bool{}
	}
	if m.warming[contextID] {
		m.warmMu.Unlock()
		return
	}
	m.warming[contextID] = true
	m.warmMu.Unlock()
	go func() {
		defer func() {
			m.warmMu.Lock()
			delete(m.warming, contextID)
			m.warmMu.Unlock()
		}()
		n, err := engine.Warm(ctx, fc)
		if err != nil {
			m.logger.Warn("knowledge: embedding warm-up stopped", zap.String("context", fc.Name), zap.Int("embedded", n), zap.Error(err))
			return
		}
		if n > 0 {
			m.logger.Info("knowledge: embeddings warmed", zap.String("context", fc.Name), zap.Int("embedded", n))
		}
	}()
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
	defer m.warmAfterAttach(contextID, opts)
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
		RetrievalTopK:  opts.RetrievalTopK,
		Weight:         opts.Weight,
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
