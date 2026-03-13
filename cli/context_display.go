/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/diillson/chatcli/cli/ctxmgr"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/utils"
)

// handleList lista contextos com suporte a filtros.
// Flags: --tag <tag>, --mode <mode>, --name <pattern>, --since <YYYY-MM-DD>, --before <YYYY-MM-DD>
func (h *ContextHandler) handleList(args []string) error {
	filter := &ctxmgr.ContextFilter{}

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--tag":
			if i+1 < len(args) {
				i++
				filter.Tags = append(filter.Tags, args[i])
			}
		case "--mode":
			if i+1 < len(args) {
				i++
				filter.Mode = ctxmgr.ProcessingMode(args[i])
			}
		case "--name":
			if i+1 < len(args) {
				i++
				filter.NamePattern = args[i]
			}
		case "--since":
			if i+1 < len(args) {
				i++
				if t, err := time.Parse("2006-01-02", args[i]); err == nil {
					filter.CreatedAfter = &t
				}
			}
		case "--before":
			if i+1 < len(args) {
				i++
				if t, err := time.Parse("2006-01-02", args[i]); err == nil {
					filter.CreatedBefore = &t
				}
			}
		}
	}

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
			fmt.Printf("    %s %s\n", colorize(i18n.T("context.display.label.description"), ColorGray), ctx.Description)
		}

		fmt.Printf("    %s %s | %s %d | %s %.2f MB\n",
			colorize(i18n.T("context.display.label.mode"), ColorGray), ctx.Mode,
			colorize(i18n.T("context.display.label.files"), ColorGray), ctx.FileCount,
			colorize(i18n.T("context.display.label.size"), ColorGray), float64(ctx.TotalSize)/1024/1024)

		if len(ctx.Tags) > 0 {
			fmt.Printf("    %s %s\n", colorize(i18n.T("context.display.label.tags"), ColorGray), strings.Join(ctx.Tags, ", "))
		}

		fmt.Printf("    %s %s\n", colorize(i18n.T("context.display.label.created"), ColorGray), ctx.CreatedAt.Format("2006-01-02 15:04"))
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

// handleShowAttached mostra contextos anexados à sessão com estimativa de tokens
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

	var totalTokens int64
	for i, ctx := range contexts {
		// Estimate tokens: ~4 chars per token (conservative)
		estimatedTokens := ctx.TotalSize / 4
		totalTokens += estimatedTokens

		fmt.Printf("\n%s %s\n",
			colorize(fmt.Sprintf("[%d]", i+1), ColorCyan),
			colorize(ctx.Name, ColorLime))

		fmt.Printf("    %s %d | %s %.2f MB | %s ~%s tokens\n",
			colorize(i18n.T("context.display.label.files"), ColorGray), ctx.FileCount,
			colorize(i18n.T("context.display.label.size"), ColorGray), float64(ctx.TotalSize)/1024/1024,
			colorize(i18n.T("context.display.label.tokens"), ColorGray), formatTokenCount(estimatedTokens))
	}

	fmt.Println()
	fmt.Println(colorize(strings.Repeat("─", 60), ColorGray))
	fmt.Printf("  %s ~%s tokens/turno\n",
		colorize(i18n.T("context.display.total_injected"), ColorCyan+ColorBold),
		colorize(formatTokenCount(totalTokens), ColorYellow))
	fmt.Printf("  %s %s\n",
		colorize("💡", ""),
		i18n.T("context.display.cache_info"))
	fmt.Printf("     %s %s\n",
		colorize("•", ColorGray),
		i18n.T("context.display.cache_anthropic"))
	fmt.Printf("     %s %s\n",
		colorize("•", ColorGray),
		i18n.T("context.display.cache_openai"))

	if totalTokens > 10000 {
		fmt.Printf("\n  %s %s\n",
			colorize("⚠", ColorYellow),
			i18n.T("context.display.large_context_warning", contexts[0].Name))
	}

	fmt.Println()
	return nil
}

// handleMetrics mostra métricas de contextos
func (h *ContextHandler) handleMetrics() error {
	metrics := h.manager.GetMetrics()

	fmt.Println(colorize(i18n.T("context.metrics.header"), ColorLime+ColorBold))
	fmt.Println(colorize(strings.Repeat("─", 80), ColorGray))

	fmt.Printf("\n  %s %d\n", colorize(i18n.T("context.display.metrics.total_contexts"), ColorCyan), metrics.TotalContexts)
	fmt.Printf("  %s %d\n", colorize(i18n.T("context.display.metrics.attached_contexts"), ColorCyan), metrics.AttachedContexts)
	fmt.Printf("  %s %d\n", colorize(i18n.T("context.display.metrics.total_files"), ColorCyan), metrics.TotalFiles)
	fmt.Printf("  %s %.2f MB\n", colorize(i18n.T("context.display.metrics.total_size"), ColorCyan),
		float64(metrics.TotalSizeBytes)/1024/1024)

	if len(metrics.ContextsByMode) > 0 {
		fmt.Printf("\n  %s\n", colorize(i18n.T("context.display.metrics.by_mode"), ColorCyan))
		for mode, count := range metrics.ContextsByMode {
			fmt.Printf("    %s: %d\n", mode, count)
		}
	}

	fmt.Printf("\n  %s %s\n", colorize(i18n.T("context.display.metrics.storage_path"), ColorGray), metrics.StoragePath)
	fmt.Println()

	return nil
}

// formatTokenCount formats a token count with K/M suffixes for readability.
func formatTokenCount(tokens int64) string {
	if tokens >= 1_000_000 {
		return fmt.Sprintf("%.1fM", float64(tokens)/1_000_000)
	}
	if tokens >= 1_000 {
		return fmt.Sprintf("%.1fK", float64(tokens)/1_000)
	}
	return fmt.Sprintf("%d", tokens)
}

// printContextInfo imprime informações detalhadas de um contexto
func (h *ContextHandler) printContextInfo(ctx *ctxmgr.FileContext, detailed bool) {
	fmt.Println(colorize(strings.Repeat("─", 80), ColorGray))
	fmt.Printf("\n%s %s\n", colorize(i18n.T("context.display.info.context_label"), ColorCyan), colorize(ctx.Name, ColorLime+ColorBold))
	fmt.Printf("%s %s\n", colorize(i18n.T("context.display.info.id_label"), ColorGray), ctx.ID)

	if ctx.Description != "" {
		fmt.Printf("%s %s\n", colorize(i18n.T("context.display.label.description"), ColorCyan), ctx.Description)
	}

	// Informações básicas
	fmt.Printf("\n%s\n", colorize(i18n.T("context.display.info.general_header"), ColorLime+ColorBold))
	fmt.Printf("%s %s\n", colorize(i18n.T("context.display.info.mode_label"), ColorCyan), ctx.Mode)
	fmt.Printf("%s %s\n",
		colorize(i18n.T("context.display.info.content_label"), ColorCyan),
		i18n.T("context.display.info.content_value", ctx.FileCount, float64(ctx.TotalSize)/1024/1024))

	if len(ctx.Tags) > 0 {
		fmt.Printf("%s %s\n", colorize(i18n.T("context.display.info.tags_label"), ColorCyan), strings.Join(ctx.Tags, ", "))
	}

	fmt.Printf("%s %s\n", colorize(i18n.T("context.display.info.created_at"), ColorGray),
		ctx.CreatedAt.Format("02/01/2006 15:04:05"))
	fmt.Printf("%s %s\n", colorize(i18n.T("context.display.info.updated_at"), ColorGray),
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
		fmt.Printf("\n%s\n", colorize(i18n.T("context.display.stats.type_distribution_header"), ColorLime+ColorBold))

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
			fmt.Printf("  %s %s %s\n",
				colorize("●", ColorCyan),
				colorize(fmt.Sprintf("%-15s", s.name+":"), ColorGray),
				i18n.T("context.display.stats.type_line", s.count, percentage, float64(s.size)/1024))
		}
	}
}

// printChunkedStructure exibe estrutura detalhada para contextos chunked
func (h *ContextHandler) printChunkedStructure(ctx *ctxmgr.FileContext, detailed bool) {
	fmt.Printf("\n%s\n", colorize(i18n.T("context.display.chunks.structure_header"), ColorLime+ColorBold))
	fmt.Printf("  %s %s\n", colorize(i18n.T("context.display.chunks.strategy"), ColorCyan), ctx.ChunkStrategy)
	fmt.Printf("  %s %s\n", colorize(i18n.T("context.display.chunks.total"), ColorCyan), i18n.T("context.display.chunks.total_value", len(ctx.Chunks)))

	if !detailed {
		fmt.Printf("\n  %s\n\n", colorize(i18n.T("context.display.chunks.detail_tip"), ColorGray))
		return
	}

	// Mostrar cada chunk em detalhe
	for i, chunk := range ctx.Chunks {
		fmt.Printf("\n  %s %s\n",
			colorize("📦", ColorYellow),
			i18n.T("context.display.chunks.chunk_label", chunk.Index, chunk.TotalChunks))

		if chunk.Description != "" {
			fmt.Printf("    %s %s\n", colorize(i18n.T("context.display.label.description"), ColorGray), chunk.Description)
		}

		fmt.Printf("    %s %s\n",
			colorize(i18n.T("context.display.chunks.content_label"), ColorGray),
			i18n.T("context.display.chunks.content_value", len(chunk.Files), float64(chunk.TotalSize)/1024, chunk.EstTokens))

		// Mostrar árvore de arquivos do chunk
		if len(chunk.Files) > 0 {
			fmt.Printf("    %s\n", colorize(i18n.T("context.display.label.files"), ColorGray))
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

	fmt.Printf("\n%s\n", colorize(i18n.T("context.display.files.structure_header"), ColorLime+ColorBold))

	if !detailed && len(ctx.Files) > 20 {
		fmt.Printf("  %s %s\n\n",
			colorize("●", ColorCyan),
			i18n.T("context.display.files.first_n_of_total", 20, len(ctx.Files)))
		h.printFileTree(ctx.Files[:20], "  ")
		fmt.Printf("\n  %s\n", colorize(
			i18n.T("context.display.files.and_more", len(ctx.Files)-20),
			ColorGray))
		fmt.Printf("  %s\n\n", colorize(
			i18n.T("context.display.files.show_all_tip"),
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
			dirName = i18n.T("context.display.files.root_dir")
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
	fmt.Printf("\n%s\n", colorize(i18n.T("context.display.attachment.header"), ColorLime+ColorBold))

	sessions := h.manager.GetSessionsForContext(ctx.ID)

	if len(sessions) > 0 {
		fmt.Printf("  %s %s\n",
			colorize("●", ColorCyan),
			i18n.T("context.display.attachment.attached_sessions", len(sessions)))
		for _, sid := range sessions {
			fmt.Printf("    %s %s\n", colorize("•", ColorGray), sid)
		}
		fmt.Println()
	} else {
		fmt.Printf("  %s %s\n",
			colorize("●", ColorGray),
			i18n.T("context.display.attachment.not_attached"))
		fmt.Println()
	}

	fmt.Printf("  %s %s\n",
		colorize("●", ColorCyan),
		i18n.T("context.display.attachment.attach_instruction"))
	fmt.Printf("    %s\n\n",
		colorize(fmt.Sprintf("/context attach %s", ctx.Name), ColorYellow))

	if ctx.IsChunked {
		fmt.Printf("  %s %s\n",
			colorize("💡", ColorCyan),
			i18n.T("context.display.attachment.chunked_info"))
		fmt.Printf("    %s %s\n", colorize("•", ColorGray),
			i18n.T("context.display.attachment.attach_all"))
		fmt.Printf("    %s %s %s\n",
			colorize("•", ColorGray),
			i18n.T("context.display.attachment.attach_specific"),
			colorize(fmt.Sprintf("/context attach %s --chunks 1,2,3", ctx.Name), ColorYellow))
		fmt.Println()
	}
}

// showContextHelp mostra ajuda do comando /context
func (h *ContextHandler) showContextHelp() {
	help := `
        ` + colorize(i18n.T("context.help.title"), ColorLime+ColorBold) + `
        ` + colorize(strings.Repeat("─", 80), ColorGray) + `

        ` + colorize(i18n.T("context.help.create_header"), ColorCyan) + `
          /context create <` + i18n.T("context.help.arg.name") + `> <` + i18n.T("context.help.arg.paths") + `> [` + i18n.T("context.help.arg.options") + `]
            --mode, -m <` + i18n.T("context.help.arg.mode") + `>           ` + i18n.T("context.help.mode_desc") + `
            --description, -d <` + i18n.T("context.help.arg.text") + `>   ` + i18n.T("context.help.description_desc") + `
            --tags, -t <tag1,tag2>      ` + i18n.T("context.help.tags_desc") + `
            --force, -f                 ` + i18n.T("context.help.force_desc") + `

          ` + colorize(i18n.T("context.help.example_label"), ColorGray) + `
            /context create projeto-api ./src ./tests --mode smart --tags api,golang

        ` + colorize(i18n.T("context.help.update_header"), ColorCyan) + `
          /context update <` + i18n.T("context.help.arg.name") + `> [<` + i18n.T("context.help.arg.paths") + `>] [` + i18n.T("context.help.arg.options") + `]
            --mode, -m <` + i18n.T("context.help.arg.mode") + `>           ` + i18n.T("context.help.new_mode_desc") + `
            --description, -d <` + i18n.T("context.help.arg.text") + `>   ` + i18n.T("context.help.new_description_desc") + `
            --tags, -t <tag1,tag2>      ` + i18n.T("context.help.new_tags_desc") + `

          ` + colorize(i18n.T("context.help.notes_label"), ColorGray) + `
            ` + i18n.T("context.help.update_note1") + `
            ` + i18n.T("context.help.update_note2") + `
            ` + i18n.T("context.help.update_note3") + `

        ` + colorize(i18n.T("context.help.attach_header"), ColorCyan) + `
          /context attach <` + i18n.T("context.help.arg.name") + `> [--priority <n>]   ` + i18n.T("context.help.attach_desc") + `
          /context detach <` + i18n.T("context.help.arg.name") + `>                     ` + i18n.T("context.help.detach_desc") + `

        ` + colorize(i18n.T("context.help.list_view_header"), ColorCyan) + `
          /context list                  ` + i18n.T("context.help.list_desc") + `
          /context show <` + i18n.T("context.help.arg.name") + `>           ` + i18n.T("context.help.show_desc") + `
          /context inspect <` + i18n.T("context.help.arg.name") + `>        ` + i18n.T("context.help.inspect_desc") + `
          /context inspect <` + i18n.T("context.help.arg.name") + `> --chunk N   ` + i18n.T("context.help.inspect_chunk_desc") + `
          /context attached              ` + i18n.T("context.help.attached_desc") + `

        ` + colorize(i18n.T("context.help.example_label"), ColorGray) + `
          /context show meu-projeto
          /context inspect meu-projeto --chunk 1

        ` + colorize(i18n.T("context.help.manage_header"), ColorCyan) + `
          /context delete <` + i18n.T("context.help.arg.name") + `>                      ` + i18n.T("context.help.delete_desc") + `
          /context merge <` + i18n.T("context.help.arg.new_name") + `> <ctx1> <ctx2>... ` + i18n.T("context.help.merge_desc") + `

        ` + colorize(i18n.T("context.help.import_export_header"), ColorCyan) + `
          /context export <` + i18n.T("context.help.arg.name") + `> <` + i18n.T("context.help.arg.path") + `>   ` + i18n.T("context.help.export_desc") + `
          /context import <` + i18n.T("context.help.arg.path") + `>          ` + i18n.T("context.help.import_desc") + `

        ` + colorize(i18n.T("context.help.metrics_header"), ColorCyan) + `
          /context metrics               ` + i18n.T("context.help.metrics_desc") + `

        ` + colorize(i18n.T("context.help.notes_label"), ColorGray) + `
          ` + i18n.T("context.help.note1") + `
          ` + i18n.T("context.help.note2") + `
          ` + i18n.T("context.help.note3") + `
        `
	fmt.Println(help)
}
