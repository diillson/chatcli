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
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/diillson/chatcli/cli/ctxmgr"
	"github.com/diillson/chatcli/i18n"
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
	fmt.Printf("\n%s %s\n", colorize("Nome:", ColorCyan), colorize(ctx.Name, ColorLime+ColorBold))
	fmt.Printf("%s %s\n", colorize("ID:", ColorGray), ctx.ID)

	if ctx.Description != "" {
		fmt.Printf("%s %s\n", colorize("Descrição:", ColorCyan), ctx.Description)
	}

	fmt.Printf("%s %s\n", colorize("Modo:", ColorCyan), ctx.Mode)

	// Mostrar informações de estrutura
	if ctx.IsChunked && len(ctx.Chunks) > 0 {
		fmt.Printf("%s %s (%d chunks)\n",
			colorize("Estrutura:", ColorCyan),
			colorize("Dividido em Chunks", ColorYellow),
			len(ctx.Chunks))

		// CORREÇÃO: Mostrar estratégia apenas se realmente houver chunks
		if ctx.ChunkStrategy != "" {
			fmt.Printf("%s %s\n", colorize("Estratégia de Chunking:", ColorCyan), ctx.ChunkStrategy)
		}

		if detailed {
			fmt.Printf("\n%s\n", colorize("Chunks:", ColorCyan))
			for _, chunk := range ctx.Chunks {
				fmt.Printf("  %s Chunk %d/%d - %s\n",
					colorize("📦", ColorYellow),
					chunk.Index,
					chunk.TotalChunks,
					chunk.Description)
				fmt.Printf("     %s %d arquivos | %s %.2f KB | %s ~%d tokens\n",
					colorize("Arquivos:", ColorGray), len(chunk.Files),
					colorize("Tamanho:", ColorGray), float64(chunk.TotalSize)/1024,
					colorize("Tokens:", ColorGray), chunk.EstTokens)
			}
		}
	} else {
		// Modo não-chunked
		fmt.Printf("%s %d arquivos\n", colorize("Arquivos:", ColorCyan), ctx.FileCount)
	}

	fmt.Printf("%s %.2f MB (%d bytes)\n", colorize("Tamanho:", ColorCyan),
		float64(ctx.TotalSize)/1024/1024, ctx.TotalSize)

	if len(ctx.Tags) > 0 {
		fmt.Printf("%s %s\n", colorize("Tags:", ColorCyan), strings.Join(ctx.Tags, ", "))
	}

	fmt.Printf("%s %s\n", colorize("Criado em:", ColorGray), ctx.CreatedAt.Format(time.RFC3339))
	fmt.Printf("%s %s\n", colorize("Atualizado em:", ColorGray), ctx.UpdatedAt.Format(time.RFC3339))

	if detailed && !ctx.IsChunked && len(ctx.Files) > 0 {
		fmt.Printf("\n%s\n", colorize("Arquivos:", ColorCyan))
		for i, file := range ctx.Files {
			if i >= 10 {
				fmt.Printf("  ... e mais %d arquivos\n", len(ctx.Files)-10)
				break
			}
			fmt.Printf("  %s %s (%s, %.2f KB)\n",
				colorize(fmt.Sprintf("[%d]", i+1), ColorGray),
				file.Path, file.Type, float64(file.Size)/1024)
		}
	}

	fmt.Println()
}

// showContextHelp mostra ajuda do comando /context
func (h *ContextHandler) showContextHelp() {
	help := `
        ` + colorize("📦 GERENCIAMENTO DE CONTEXTOS", ColorLime+ColorBold) + `
        ` + colorize(strings.Repeat("─", 80), ColorGray) + `
        
        ` + colorize("CRIAR CONTEXTO:", ColorCyan) + `
          /context create <nome> <caminhos...> [opções]
            --mode, -m <modo>           Modo: full, summary, chunked, smart
            --description, -d <texto>   Descrição do contexto
            --tags, -t <tag1,tag2>      Tags separadas por vírgula
            
          Exemplo:
            /context create projeto-api ./src ./tests --mode smart --tags api,golang
        
        ` + colorize("ANEXAR/DESANEXAR:", ColorCyan) + `
          /context attach <nome> [--priority <n>]   Anexa contexto à sessão atual
          /context detach <nome>                     Desanexa contexto da sessão
        
        ` + colorize("LISTAR E VISUALIZAR:", ColorCyan) + `
          /context list                  Lista todos os contextos
          /context show <nome>           Mostra detalhes de um contexto
          /context attached              Mostra contextos anexados à sessão
        
        ` + colorize("GERENCIAR:", ColorCyan) + `
          /context delete <nome>                      Deleta um contexto
          /context merge <novo-nome> <ctx1> <ctx2>... Mescla múltiplos contextos
        
        ` + colorize("IMPORTAR/EXPORTAR:", ColorCyan) + `
          /context export <nome> <caminho>   Exporta contexto para arquivo
          /context import <caminho>          Importa contexto de arquivo
        
        ` + colorize("MÉTRICAS:", ColorCyan) + `
          /context metrics               Mostra estatísticas de uso
        
        ` + colorize("NOTAS:", ColorGray) + `
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
