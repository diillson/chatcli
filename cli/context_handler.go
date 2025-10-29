/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
 */
package cli

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/diillson/chatcli/cli/ctxmgr"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/utils"
	"go.uber.org/zap"
)

// ContextHandler gerencia comandos relacionados a contextos
type ContextHandler struct {
	manager *ctxmgr.Manager
	logger  *zap.Logger
}

// NewContextHandler cria um novo handler de contextos
func NewContextHandler(logger *zap.Logger) (*ContextHandler, error) {
	manager, err := ctxmgr.NewManager(logger)
	if err != nil {
		return nil, fmt.Errorf("erro ao inicializar gerenciador de contextos: %w", err)
	}

	return &ContextHandler{
		manager: manager,
		logger:  logger,
	}, nil
}

// HandleContextCommand processa comandos /context
func (h *ContextHandler) HandleContextCommand(sessionID, input string) error {
	parts := strings.Fields(input)
	if len(parts) < 2 {
		h.showContextHelp()
		return nil
	}

	subcommand := strings.ToLower(parts[1])

	switch subcommand {
	case "create", "new":
		return h.handleCreate(parts[2:])

	case "attach", "add":
		return h.handleAttach(sessionID, parts[2:])

	case "detach", "remove":
		return h.handleDetach(sessionID, parts[2:])

	case "delete", "del", "rm":
		return h.handleDelete(parts[2:])

	case "list", "ls":
		return h.handleList(parts[2:])

	case "show", "info", "view":
		return h.handleShow(parts[2:])

	case "inspect":
		return h.handleInspect(parts[2:])

	case "merge", "join":
		return h.handleMerge(parts[2:])

	case "attached":
		return h.handleShowAttached(sessionID)

	case "export":
		return h.handleExport(parts[2:])

	case "import":
		return h.handleImport(parts[2:])

	case "metrics", "stats":
		return h.handleMetrics()

	case "help", "?":
		h.showContextHelp()
		return nil

	default:
		return fmt.Errorf("%s", i18n.T("context.error.unknown_subcommand", subcommand))
	}
}

// handleCreate cria um novo contexto
func (h *ContextHandler) handleCreate(args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("%s", i18n.T("context.create.usage"))
	}

	// Parser de argumentos melhorado
	var name, description, modeStr string
	var paths []string
	var tags []string

	// Primeiro argumento é o nome
	name = args[0]

	// Processar flags e paths
	i := 1
	for i < len(args) {
		arg := args[i]

		switch {
		case arg == "--mode" || arg == "-m":
			if i+1 >= len(args) {
				return fmt.Errorf("%s", i18n.T("context.create.error.mode_required"))
			}
			i++ // Avançar para o valor
			modeStr = args[i]
			i++ // Avançar para o próximo argumento

		case arg == "--description" || arg == "--desc" || arg == "-d":
			if i+1 >= len(args) {
				return fmt.Errorf("%s", i18n.T("context.create.error.description_required"))
			}
			i++ // Avançar para o valor
			description = args[i]
			i++ // Avançar para o próximo argumento

		case arg == "--tags" || arg == "-t":
			if i+1 >= len(args) {
				return fmt.Errorf("%s", i18n.T("context.create.error.tags_required"))
			}
			i++ // Avançar para o valor
			tags = strings.Split(args[i], ",")
			i++ // Avançar para o próximo argumento

		case strings.HasPrefix(arg, "--") || strings.HasPrefix(arg, "-"):
			// Flag desconhecida
			return fmt.Errorf("flag desconhecida: %s", arg)

		default:
			// É um path
			paths = append(paths, arg)
			i++
		}
	}

	if len(paths) == 0 {
		return fmt.Errorf("%s", i18n.T("context.create.error.no_paths"))
	}

	// Modo padrão
	mode := ctxmgr.ModeFull
	if modeStr != "" {
		mode = ctxmgr.ProcessingMode(strings.ToLower(modeStr))

		// Validar modo
		validModes := map[ctxmgr.ProcessingMode]bool{
			ctxmgr.ModeFull:    true,
			ctxmgr.ModeSummary: true,
			ctxmgr.ModeChunked: true,
			ctxmgr.ModeSmart:   true,
		}

		if !validModes[mode] {
			return fmt.Errorf("modo inválido: %s (use: full, summary, chunked, smart)", modeStr)
		}
	}

	// Limpar tags
	for i := range tags {
		tags[i] = strings.TrimSpace(tags[i])
	}

	fmt.Println(i18n.T("context.create.processing"))

	// Debug: mostrar o que vai ser processado
	fmt.Printf("  Nome: %s\n", name)
	fmt.Printf("  Modo: %s\n", mode)
	fmt.Printf("  Paths: %v\n", paths)
	if description != "" {
		fmt.Printf("  Descrição: %s\n", description)
	}
	if len(tags) > 0 {
		fmt.Printf("  Tags: %v\n", tags)
	}
	fmt.Println()

	ctx, err := h.manager.CreateContext(name, description, paths, mode, tags)
	if err != nil {
		return fmt.Errorf("%s", i18n.T("context.create.error.failed", err))
	}

	fmt.Println(colorize(i18n.T("context.create.success"), ColorGreen))
	h.printContextInfo(ctx, false)

	return nil
}

// handleAttach anexa um contexto à sessão atual
func (h *ContextHandler) handleAttach(sessionID string, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("%s", i18n.T("context.attach.usage"))
	}

	contextName := args[0]
	priority := 100          // Prioridade padrão
	var selectedChunks []int // Chunks específicos (vazio = todos)

	// Processar flags
	i := 1
	for i < len(args) {
		arg := args[i]

		switch {
		case arg == "--priority" || arg == "-p":
			if i+1 >= len(args) {
				return fmt.Errorf("%s", i18n.T("context.attach.error.invalid_priority"))
			}
			i++
			p, err := strconv.Atoi(args[i])
			if err != nil {
				return fmt.Errorf("%s", i18n.T("context.attach.error.invalid_priority"))
			}
			priority = p
			i++

		case arg == "--chunk" || arg == "-c":
			if i+1 >= len(args) {
				return fmt.Errorf("--chunk requer um número (ex: --chunk 1)")
			}
			i++
			chunkNum, err := strconv.Atoi(args[i])
			if err != nil {
				return fmt.Errorf("número de chunk inválido: %s", args[i])
			}
			selectedChunks = append(selectedChunks, chunkNum)
			i++

		case arg == "--chunks" || arg == "-C":
			if i+1 >= len(args) {
				return fmt.Errorf("--chunks requer números separados por vírgula (ex: --chunks 1,2,3)")
			}
			i++
			// Parse "1,2,3"
			parts := strings.Split(args[i], ",")
			for _, part := range parts {
				chunkNum, err := strconv.Atoi(strings.TrimSpace(part))
				if err != nil {
					return fmt.Errorf("número de chunk inválido: %s", part)
				}
				selectedChunks = append(selectedChunks, chunkNum)
			}
			i++

		default:
			return fmt.Errorf("flag desconhecida: %s", arg)
		}
	}

	// Buscar contexto por nome
	ctx, err := h.manager.GetContextByName(contextName)
	if err != nil {
		return fmt.Errorf("%s", i18n.T("context.attach.error.not_found", contextName))
	}

	// Validar chunks selecionados
	if len(selectedChunks) > 0 {
		if !ctx.IsChunked {
			return fmt.Errorf("o contexto '%s' não está dividido em chunks", contextName)
		}

		// Validar que os chunks existem
		for _, chunkNum := range selectedChunks {
			if chunkNum < 1 || chunkNum > len(ctx.Chunks) {
				return fmt.Errorf("chunk %d não existe (disponíveis: 1-%d)", chunkNum, len(ctx.Chunks))
			}
		}
	}

	// Anexar com opções de chunk
	attachOpts := ctxmgr.AttachOptions{
		Priority:       priority,
		SelectedChunks: selectedChunks,
	}

	if err := h.manager.AttachContextWithOptions(sessionID, ctx.ID, attachOpts); err != nil {
		return fmt.Errorf("%s", i18n.T("context.attach.error.failed", err))
	}

	// Feedback detalhado
	fmt.Println(colorize(fmt.Sprintf("✅ Contexto '%s' anexado com sucesso!", ctx.Name), ColorGreen))
	fmt.Printf("  %s %d\n", colorize("Prioridade:", ColorCyan), priority)

	if len(selectedChunks) > 0 {
		fmt.Printf("  %s %v de %d\n",
			colorize("Chunks selecionados:", ColorCyan),
			selectedChunks,
			len(ctx.Chunks))

		// Mostrar detalhes dos chunks selecionados
		var totalFiles int
		var totalSize int64
		for _, chunkNum := range selectedChunks {
			chunk := ctx.Chunks[chunkNum-1] // índice base-0
			totalFiles += len(chunk.Files)
			totalSize += chunk.TotalSize
			fmt.Printf("    📦 Chunk %d: %s (%d arquivos, %.2f KB)\n",
				chunkNum, chunk.Description, len(chunk.Files), float64(chunk.TotalSize)/1024)
		}
		fmt.Printf("  %s %d arquivos | %.2f MB\n",
			colorize("Total anexado:", ColorCyan),
			totalFiles, float64(totalSize)/1024/1024)
	} else {
		if ctx.IsChunked {
			fmt.Printf("  %s Todos os %d chunks (%d arquivos, %.2f MB)\n",
				colorize("Anexado:", ColorCyan),
				len(ctx.Chunks), ctx.FileCount, float64(ctx.TotalSize)/1024/1024)
		} else {
			fmt.Printf("  %s %d arquivos | %.2f MB\n",
				colorize("Anexado:", ColorCyan),
				ctx.FileCount, float64(ctx.TotalSize)/1024/1024)
		}
	}

	return nil
}

// handleDetach desanexa um contexto da sessão
func (h *ContextHandler) handleDetach(sessionID string, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("%s", i18n.T("context.detach.usage"))
	}

	contextName := args[0]

	// Buscar contexto por nome
	ctx, err := h.manager.GetContextByName(contextName)
	if err != nil {
		return fmt.Errorf("%s", i18n.T("context.detach.error.not_found", contextName))
	}

	// Desanexar
	if err := h.manager.DetachContext(sessionID, ctx.ID); err != nil {
		return fmt.Errorf("%s", i18n.T("context.detach.error.failed", err))
	}

	fmt.Println(colorize(i18n.T("context.detach.success", ctx.Name), ColorGreen))

	return nil
}

// handleDelete deleta um contexto permanentemente
func (h *ContextHandler) handleDelete(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("%s", i18n.T("context.delete.usage"))
	}

	contextName := args[0]

	// Buscar contexto por nome
	ctx, err := h.manager.GetContextByName(contextName)
	if err != nil {
		return fmt.Errorf("%s", i18n.T("context.delete.error.not_found", contextName))
	}

	fmt.Printf("%s", i18n.T("context.delete.confirm", ctx.Name))

	// Restaurar terminal antes de ler input
	if runtime.GOOS != "windows" {
		cmd := exec.Command("stty", "sane")
		cmd.Stdin = os.Stdin
		_ = cmd.Run()
	}

	reader := bufio.NewReader(os.Stdin)
	response, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("erro ao ler resposta: %w", err)
	}

	if strings.ToLower(strings.TrimSpace(response)) != "s" &&
		strings.ToLower(strings.TrimSpace(response)) != "y" {
		fmt.Println(i18n.T("context.delete.cancelled"))
		return nil
	}

	// Deletar
	if err := h.manager.DeleteContext(ctx.ID); err != nil {
		return fmt.Errorf("%s", i18n.T("context.delete.error.failed", err))
	}

	fmt.Println(colorize(i18n.T("context.delete.success", ctx.Name), ColorGreen))

	return nil
}

// handleList lista contextos
func (h *ContextHandler) handleList(args []string) error {
	// TODO: Implementar filtros baseado em args
	filter := &ctxmgr.ContextFilter{}

	contexts, err := h.manager.ListContexts(filter)
	if err != nil {
		return fmt.Errorf("%s", i18n.T("context.list.error.failed", err))
	}

	if len(contexts) == 0 {
		fmt.Println(i18n.T("context.list.empty"))
		return nil
	}

	fmt.Println(colorize(i18n.T("context.list.header"), ColorLime+ColorBold))
	fmt.Println(colorize(strings.Repeat("─", 80), ColorGray))

	for i, ctx := range contexts {
		fmt.Printf("\n%s %s\n",
			colorize(fmt.Sprintf("[%d]", i+1), ColorCyan),
			colorize(ctx.Name, ColorLime))

		if ctx.Description != "" {
			fmt.Printf("    %s %s\n", colorize("Descrição:", ColorGray), ctx.Description)
		}

		fmt.Printf("    %s %s | %s %d | %s %.2f MB\n",
			colorize("Modo:", ColorGray), ctx.Mode,
			colorize("Arquivos:", ColorGray), ctx.FileCount,
			colorize("Tamanho:", ColorGray), float64(ctx.TotalSize)/1024/1024)

		if len(ctx.Tags) > 0 {
			fmt.Printf("    %s %s\n", colorize("Tags:", ColorGray), strings.Join(ctx.Tags, ", "))
		}

		fmt.Printf("    %s %s\n", colorize("Criado:", ColorGray), ctx.CreatedAt.Format("2006-01-02 15:04"))
	}

	fmt.Println()
	return nil
}

// handleShow mostra detalhes de um contexto
func (h *ContextHandler) handleShow(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("%s", i18n.T("context.show.usage"))
	}

	contextName := args[0]

	ctx, err := h.manager.GetContextByName(contextName)
	if err != nil {
		return fmt.Errorf("%s", i18n.T("context.show.error.not_found", contextName))
	}

	h.printContextInfo(ctx, true)

	return nil
}

// handleInspect fornece inspeção profunda de um contexto
func (h *ContextHandler) handleInspect(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("Uso: /context inspect <nome> [--chunk N]")
	}

	contextName := args[0]
	chunkNum := -1

	// Parse flags
	for i := 1; i < len(args); i++ {
		if args[i] == "--chunk" && i+1 < len(args) {
			n, err := strconv.Atoi(args[i+1])
			if err == nil {
				chunkNum = n
			}
			i++
		}
	}

	ctx, err := h.manager.GetContextByName(contextName)
	if err != nil {
		return fmt.Errorf("Contexto '%s' não encontrado", contextName)
	}

	// Se chunk específico foi solicitado
	if chunkNum > 0 && ctx.IsChunked {
		if chunkNum > len(ctx.Chunks) {
			return fmt.Errorf("Chunk %d não existe (disponíveis: 1-%d)",
				chunkNum, len(ctx.Chunks))
		}
		h.inspectChunk(ctx, chunkNum-1)
		return nil
	}

	// Inspeção geral
	h.inspectContext(ctx)
	return nil
}

// inspectContext mostra análise detalhada do contexto
func (h *ContextHandler) inspectContext(ctx *ctxmgr.FileContext) {
	fmt.Println(colorize("\n🔍 INSPEÇÃO DETALHADA", ColorLime+ColorBold))
	fmt.Println(colorize(strings.Repeat("═", 80), ColorGray))

	// Estatísticas avançadas
	var totalLines int
	languageMap := make(map[string]int)
	sizeDistribution := make(map[string]int) // small, medium, large

	for _, file := range ctx.Files {
		lines := strings.Count(file.Content, "\n") + 1
		totalLines += lines

		ext := strings.ToLower(filepath.Ext(file.Path))
		languageMap[ext]++

		// Classificar por tamanho
		sizeKB := float64(file.Size) / 1024
		var sizeClass string
		switch {
		case sizeKB < 10:
			sizeClass = "pequeno (<10KB)"
		case sizeKB < 100:
			sizeClass = "médio (10-100KB)"
		default:
			sizeClass = "grande (>100KB)"
		}
		sizeDistribution[sizeClass]++
	}

	fmt.Printf("\n%s\n", colorize("📊 ANÁLISE ESTATÍSTICA", ColorCyan+ColorBold))
	fmt.Printf("  Total de linhas de código: %s\n",
		colorize(fmt.Sprintf("%d", totalLines), ColorYellow))
	fmt.Printf("  Média de linhas por arquivo: %s\n",
		colorize(fmt.Sprintf("%.0f", float64(totalLines)/float64(ctx.FileCount)), ColorYellow))

	fmt.Printf("\n%s\n", colorize("📐 DISTRIBUIÇÃO DE TAMANHO", ColorCyan+ColorBold))
	for size, count := range sizeDistribution {
		percentage := float64(count) / float64(ctx.FileCount) * 100
		fmt.Printf("  %s: %d arquivos (%.1f%%)\n", size, count, percentage)
	}

	fmt.Printf("\n%s\n", colorize("🗂️ EXTENSÕES ENCONTRADAS", ColorCyan+ColorBold))
	var exts []string
	for ext := range languageMap {
		exts = append(exts, ext)
	}
	sort.Slice(exts, func(i, j int) bool {
		return languageMap[exts[i]] > languageMap[exts[j]]
	})

	for _, ext := range exts {
		count := languageMap[ext]
		if ext == "" {
			ext = "(sem extensão)"
		}
		fmt.Printf("  %s: %d arquivo(s)\n", ext, count)
	}

	// Análise de chunks se aplicável
	if ctx.IsChunked {
		fmt.Printf("\n%s\n", colorize("🧩 ANÁLISE DE CHUNKS", ColorCyan+ColorBold))

		var totalChunkSize int64
		var minSize, maxSize int64 = ctx.Chunks[0].TotalSize, ctx.Chunks[0].TotalSize

		for _, chunk := range ctx.Chunks {
			totalChunkSize += chunk.TotalSize
			if chunk.TotalSize < minSize {
				minSize = chunk.TotalSize
			}
			if chunk.TotalSize > maxSize {
				maxSize = chunk.TotalSize
			}
		}

		avgSize := totalChunkSize / int64(len(ctx.Chunks))

		fmt.Printf("  Tamanho médio por chunk: %.2f KB\n", float64(avgSize)/1024)
		fmt.Printf("  Menor chunk: %.2f KB\n", float64(minSize)/1024)
		fmt.Printf("  Maior chunk: %.2f KB\n", float64(maxSize)/1024)
		fmt.Printf("  Variação: %.1f%%\n",
			float64(maxSize-minSize)/float64(avgSize)*100)
	}

	fmt.Println()
}

// inspectChunk mostra detalhes de um chunk específico
func (h *ContextHandler) inspectChunk(ctx *ctxmgr.FileContext, index int) {
	chunk := ctx.Chunks[index]

	fmt.Printf("\n🔍 INSPEÇÃO DO CHUNK %d/%d\n", chunk.Index, chunk.TotalChunks)
	fmt.Println(colorize(strings.Repeat("═", 80), ColorGray))

	fmt.Printf("\n%s %s\n", colorize("Descrição:", ColorCyan), chunk.Description)
	fmt.Printf("%s %d arquivos\n", colorize("Arquivos:", ColorCyan), len(chunk.Files))
	fmt.Printf("%s %.2f KB\n", colorize("Tamanho:", ColorCyan), float64(chunk.TotalSize)/1024)
	fmt.Printf("%s ~%d tokens\n", colorize("Tokens estimados:", ColorCyan), chunk.EstTokens)

	fmt.Printf("\n%s\n", colorize("📋 LISTA COMPLETA DE ARQUIVOS", ColorCyan+ColorBold))

	for i, file := range chunk.Files {
		lines := strings.Count(file.Content, "\n") + 1
		fmt.Printf("  %d. %s\n", i+1, colorize(file.Path, ColorYellow))
		fmt.Printf("     %s %s | %s %d linhas | %s %.2f KB\n",
			colorize("Tipo:", ColorGray), file.Type,
			colorize("Linhas:", ColorGray), lines,
			colorize("Tamanho:", ColorGray), float64(file.Size)/1024)
	}

	fmt.Println()
}

// handleMerge mescla contextos
func (h *ContextHandler) handleMerge(args []string) error {
	if len(args) < 3 {
		return fmt.Errorf("%s", i18n.T("context.merge.usage"))
	}

	newName := args[0]
	contextNames := args[1:]

	// Buscar IDs dos contextos
	contextIDs := make([]string, 0, len(contextNames))
	for _, name := range contextNames {
		ctx, err := h.manager.GetContextByName(name)
		if err != nil {
			return fmt.Errorf("%s", i18n.T("context.merge.error.context_not_found", name))
		}
		contextIDs = append(contextIDs, ctx.ID)
	}

	// Opções de merge padrão
	opts := ctxmgr.MergeOptions{
		RemoveDuplicates: true,
		SortByPath:       true,
		PreferNewer:      true,
		Tags:             []string{"merged"},
	}

	fmt.Println(i18n.T("context.merge.processing"))

	mergedCtx, err := h.manager.MergeContexts(newName, "", contextIDs, opts)
	if err != nil {
		return fmt.Errorf("%s", i18n.T("context.merge.error.failed", err))
	}

	fmt.Println(colorize(i18n.T("context.merge.success"), ColorGreen))
	h.printContextInfo(mergedCtx, false)

	return nil
}

// handleShowAttached mostra contextos anexados à sessão
func (h *ContextHandler) handleShowAttached(sessionID string) error {
	contexts, err := h.manager.GetAttachedContexts(sessionID)
	if err != nil {
		return fmt.Errorf("%s", i18n.T("context.attached.error.failed", err))
	}

	if len(contexts) == 0 {
		fmt.Println(i18n.T("context.attached.empty"))
		return nil
	}

	fmt.Println(colorize(i18n.T("context.attached.header"), ColorLime+ColorBold))
	fmt.Println(colorize(strings.Repeat("─", 80), ColorGray))

	for i, ctx := range contexts {
		fmt.Printf("\n%s %s\n",
			colorize(fmt.Sprintf("[%d]", i+1), ColorCyan),
			colorize(ctx.Name, ColorLime))

		fmt.Printf("    %s %d | %s %.2f MB\n",
			colorize("Arquivos:", ColorGray), ctx.FileCount,
			colorize("Tamanho:", ColorGray), float64(ctx.TotalSize)/1024/1024)
	}

	fmt.Println()
	return nil
}

// handleExport exporta um contexto
func (h *ContextHandler) handleExport(args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("%s", i18n.T("context.export.usage"))
	}

	contextName := args[0]
	targetPath := args[1]

	ctx, err := h.manager.GetContextByName(contextName)
	if err != nil {
		return fmt.Errorf("%s", i18n.T("context.export.error.not_found", contextName))
	}

	if err := h.manager.Storage.ExportContext(ctx, targetPath); err != nil {
		return fmt.Errorf("%s", i18n.T("context.export.error.failed", err))
	}

	fmt.Println(colorize(i18n.T("context.export.success", targetPath), ColorGreen))

	return nil
}

// handleImport importa um contexto
func (h *ContextHandler) handleImport(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("%s", i18n.T("context.import.usage"))
	}

	sourcePath := args[0]

	ctx, err := h.manager.Storage.ImportContext(sourcePath)
	if err != nil {
		return fmt.Errorf("%s", i18n.T("context.import.error.failed", err))
	}

	fmt.Println(colorize(i18n.T("context.import.success", ctx.Name), ColorGreen))
	h.printContextInfo(ctx, false)

	return nil
}

// handleMetrics mostra métricas de contextos
func (h *ContextHandler) handleMetrics() error {
	metrics := h.manager.GetMetrics()

	fmt.Println(colorize(i18n.T("context.metrics.header"), ColorLime+ColorBold))
	fmt.Println(colorize(strings.Repeat("─", 80), ColorGray))

	fmt.Printf("\n  %s %d\n", colorize("Total de Contextos:", ColorCyan), metrics.TotalContexts)
	fmt.Printf("  %s %d\n", colorize("Contextos Anexados:", ColorCyan), metrics.AttachedContexts)
	fmt.Printf("  %s %d\n", colorize("Total de Arquivos:", ColorCyan), metrics.TotalFiles)
	fmt.Printf("  %s %.2f MB\n", colorize("Tamanho Total:", ColorCyan),
		float64(metrics.TotalSizeBytes)/1024/1024)

	if len(metrics.ContextsByMode) > 0 {
		fmt.Printf("\n  %s\n", colorize("Contextos por Modo:", ColorCyan))
		for mode, count := range metrics.ContextsByMode {
			fmt.Printf("    %s: %d\n", mode, count)
		}
	}

	fmt.Printf("\n  %s %s\n", colorize("Caminho de Armazenamento:", ColorGray), metrics.StoragePath)
	fmt.Println()

	return nil
}

// printContextInfo imprime informações detalhadas de um contexto
func (h *ContextHandler) printContextInfo(ctx *ctxmgr.FileContext, detailed bool) {
	fmt.Println(colorize(strings.Repeat("─", 80), ColorGray))
	fmt.Printf("\n%s %s\n", colorize("📦 Contexto:", ColorCyan), colorize(ctx.Name, ColorLime+ColorBold))
	fmt.Printf("%s %s\n", colorize("ID:", ColorGray), ctx.ID)

	if ctx.Description != "" {
		fmt.Printf("%s %s\n", colorize("Descrição:", ColorCyan), ctx.Description)
	}

	// Informações básicas
	fmt.Printf("\n%s\n", colorize("📊 INFORMAÇÕES GERAIS", ColorLime+ColorBold))
	fmt.Printf("%s %s\n", colorize("  Modo:", ColorCyan), ctx.Mode)
	fmt.Printf("%s %d arquivos | %.2f MB\n",
		colorize("  Conteúdo:", ColorCyan),
		ctx.FileCount,
		float64(ctx.TotalSize)/1024/1024)

	if len(ctx.Tags) > 0 {
		fmt.Printf("%s %s\n", colorize("  Tags:", ColorCyan), strings.Join(ctx.Tags, ", "))
	}

	fmt.Printf("%s %s\n", colorize("  Criado em:", ColorGray),
		ctx.CreatedAt.Format("02/01/2006 15:04:05"))
	fmt.Printf("%s %s\n", colorize("  Atualizado em:", ColorGray),
		ctx.UpdatedAt.Format("02/01/2006 15:04:05"))

	// Estatísticas de tipos de arquivo
	h.printFileTypeStatistics(ctx)

	// Mostrar estrutura baseada no modo
	if ctx.IsChunked && len(ctx.Chunks) > 0 {
		h.printChunkedStructure(ctx, detailed)
	} else {
		h.printFileStructure(ctx, detailed)
	}

	// Informações de anexação
	h.printAttachmentInfo(ctx)
}

// printFileTypeStatistics exibe estatísticas de tipos de arquivo
func (h *ContextHandler) printFileTypeStatistics(ctx *ctxmgr.FileContext) {
	fileTypes := make(map[string]int)
	typeSizes := make(map[string]int64)

	for _, file := range ctx.Files {
		fileTypes[file.Type]++
		typeSizes[file.Type] += file.Size
	}

	if len(fileTypes) > 0 {
		fmt.Printf("\n%s\n", colorize("📂 DISTRIBUIÇÃO POR TIPO", ColorLime+ColorBold))

		// Ordenar por quantidade
		type typeStats struct {
			name  string
			count int
			size  int64
		}
		var stats []typeStats
		for t, c := range fileTypes {
			stats = append(stats, typeStats{t, c, typeSizes[t]})
		}
		sort.Slice(stats, func(i, j int) bool {
			return stats[i].count > stats[j].count
		})

		for _, s := range stats {
			percentage := float64(s.count) / float64(ctx.FileCount) * 100
			fmt.Printf("  %s %s %d arquivos (%.1f%%) | %.2f KB\n",
				colorize("●", ColorCyan),
				colorize(fmt.Sprintf("%-15s", s.name+":"), ColorGray),
				s.count,
				percentage,
				float64(s.size)/1024)
		}
	}
}

// printChunkedStructure exibe estrutura detalhada para contextos chunked
func (h *ContextHandler) printChunkedStructure(ctx *ctxmgr.FileContext, detailed bool) {
	fmt.Printf("\n%s\n", colorize("🧩 ESTRUTURA EM CHUNKS", ColorLime+ColorBold))
	fmt.Printf("  %s %s\n", colorize("Estratégia:", ColorCyan), ctx.ChunkStrategy)
	fmt.Printf("  %s %d chunks\n", colorize("Total:", ColorCyan), len(ctx.Chunks))

	if !detailed {
		fmt.Printf("\n  %s\n\n", colorize("💡 Use '/context show <nome>' para ver detalhes completos dos chunks", ColorGray))
		return
	}

	// Mostrar cada chunk em detalhe
	for i, chunk := range ctx.Chunks {
		fmt.Printf("\n  %s Chunk %d/%d\n",
			colorize("📦", ColorYellow),
			chunk.Index,
			chunk.TotalChunks)

		if chunk.Description != "" {
			fmt.Printf("    %s %s\n", colorize("Descrição:", ColorGray), chunk.Description)
		}

		fmt.Printf("    %s %d arquivos | %.2f KB | ~%d tokens\n",
			colorize("Conteúdo:", ColorGray),
			len(chunk.Files),
			float64(chunk.TotalSize)/1024,
			chunk.EstTokens)

		// Mostrar árvore de arquivos do chunk
		if len(chunk.Files) > 0 {
			fmt.Printf("    %s\n", colorize("Arquivos:", ColorGray))
			h.printFileTree(chunk.Files, "      ")
		}

		// Separador entre chunks
		if i < len(ctx.Chunks)-1 {
			fmt.Println(colorize("    "+strings.Repeat("─", 70), ColorGray))
		}
	}
}

// printFileStructure exibe estrutura de arquivos para contextos não-chunked
func (h *ContextHandler) printFileStructure(ctx *ctxmgr.FileContext, detailed bool) {
	if len(ctx.Files) == 0 {
		return
	}

	fmt.Printf("\n%s\n", colorize("📁 ESTRUTURA DE ARQUIVOS", ColorLime+ColorBold))

	if !detailed && len(ctx.Files) > 20 {
		fmt.Printf("  %s Primeiros 20 de %d arquivos:\n\n",
			colorize("●", ColorCyan), len(ctx.Files))
		h.printFileTree(ctx.Files[:20], "  ")
		fmt.Printf("\n  %s\n", colorize(
			fmt.Sprintf("... e mais %d arquivos", len(ctx.Files)-20),
			ColorGray))
		fmt.Printf("  %s\n\n", colorize(
			"💡 Use '/context show <nome>' para ver todos os arquivos",
			ColorGray))
	} else {
		h.printFileTree(ctx.Files, "  ")
	}
}

// printFileTree imprime uma árvore de arquivos organizada por diretório
func (h *ContextHandler) printFileTree(files []utils.FileInfo, indent string) {
	// Organizar arquivos por diretório
	dirMap := make(map[string][]utils.FileInfo)

	for _, file := range files {
		dir := filepath.Dir(file.Path)
		dirMap[dir] = append(dirMap[dir], file)
	}

	// Ordenar diretórios
	var dirs []string
	for dir := range dirMap {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)

	// Imprimir estrutura
	for _, dir := range dirs {
		// Mostrar diretório
		dirName := dir
		if dirName == "." {
			dirName = "(raiz)"
		}
		fmt.Printf("%s%s %s/\n",
			indent,
			colorize("📂", ColorYellow),
			colorize(dirName, ColorCyan))

		// Mostrar arquivos do diretório
		filesInDir := dirMap[dir]
		sort.Slice(filesInDir, func(i, j int) bool {
			return filesInDir[i].Path < filesInDir[j].Path
		})

		for i, file := range filesInDir {
			isLast := i == len(filesInDir)-1
			prefix := "├─"
			if isLast {
				prefix = "└─"
			}

			fileName := filepath.Base(file.Path)
			fmt.Printf("%s  %s %s %s %s (%.2f KB)\n",
				indent,
				colorize(prefix, ColorGray),
				colorize("📄", ColorCyan),
				fileName,
				colorize(fmt.Sprintf("[%s]", file.Type), ColorGray),
				float64(file.Size)/1024)
		}
	}
}

// printAttachmentInfo mostra informações sobre anexações deste contexto
func (h *ContextHandler) printAttachmentInfo(ctx *ctxmgr.FileContext) {
	// Verificar se o contexto está anexado em alguma sessão
	// (isso requer acesso ao storage de attachments, vou adicionar um método helper)

	fmt.Printf("\n%s\n", colorize("📌 STATUS DE ANEXAÇÃO", ColorLime+ColorBold))

	// Por enquanto, mostrar apenas informação básica
	// Em uma implementação completa, você buscaria nas sessões ativas

	fmt.Printf("  %s Para anexar este contexto à sessão atual:\n",
		colorize("●", ColorCyan))
	fmt.Printf("    %s\n\n",
		colorize(fmt.Sprintf("/context attach %s", ctx.Name), ColorYellow))

	if ctx.IsChunked {
		fmt.Printf("  %s Este contexto está dividido em chunks. Você pode:\n",
			colorize("💡", ColorCyan))
		fmt.Printf("    %s Anexar todos os chunks\n", colorize("•", ColorGray))
		fmt.Printf("    %s Anexar chunks específicos com: %s\n",
			colorize("•", ColorGray),
			colorize(fmt.Sprintf("/context attach %s --chunks 1,2,3", ctx.Name), ColorYellow))
		fmt.Println()
	}
}

// showContextHelp mostra ajuda do comando /context
func (h *ContextHandler) showContextHelp() {
	help := `
        ` + colorize("📦 Gerenciamento de Contextos", ColorLime+ColorBold) + `
        ` + colorize(strings.Repeat("─", 80), ColorGray) + `
        
        ` + colorize("Criar Ccontexto:", ColorCyan) + `
          /context create <nome> <caminhos...> [opções]
            --mode, -m <modo>           Modo: full, summary, chunked, smart
            --description, -d <texto>   Descrição do contexto
            --tags, -t <tag1,tag2>      Tags separadas por vírgula
            
          ` + colorize("Exemplo:", ColorGray) + `
            /context create projeto-api ./src ./tests --mode smart --tags api,golang
        
        ` + colorize("Anexar/Desanexar:", ColorCyan) + `
          /context attach <nome> [--priority <n>]   Anexa contexto à sessão atual
          /context detach <nome>                     Desanexa contexto da sessão
        
        ` + colorize("Listar e Visualizar:", ColorCyan) + `
          /context list                  Lista todos os contextos
          /context show <nome>           Mostra detalhes completos de um contexto
          /context inspect <nome>        Análise estatística profunda do contexto
          /context inspect <nome> --chunk N   Inspeciona chunk específico
          /context attached              Mostra contextos anexados à sessão
        
        ` + colorize("Exemplo:", ColorGray) + `
          /context show meu-projeto
          /context inspect meu-projeto --chunk 1
        
        ` + colorize("Gerenciar:", ColorCyan) + `
          /context delete <nome>                      Deleta um contexto
          /context merge <novo-nome> <ctx1> <ctx2>... Mescla múltiplos contextos
        
        ` + colorize("Importar/Exportar:", ColorCyan) + `
          /context export <nome> <caminho>   Exporta contexto para arquivo
          /context import <caminho>          Importa contexto de arquivo
        
        ` + colorize("Métricas:", ColorCyan) + `
          /context metrics               Mostra estatísticas de uso
        
        ` + colorize("Notas:", ColorGray) + `
          • Contextos anexados são automaticamente incluídos nos prompts à LLM
          • Use prioridade para controlar a ordem (menor = primeiro)
          • Contextos mesclados removem duplicatas automaticamente
        `
	fmt.Println(help)
}

// GetManager retorna o gerenciador de contextos
func (h *ContextHandler) GetManager() *ctxmgr.Manager {
	return h.manager
}
