/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/diillson/chatcli/cli/ctxmgr"
	"github.com/diillson/chatcli/i18n"
)

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
				return fmt.Errorf("%s", i18n.T("context.io.error.chunk_requires_number"))
			}
			i++
			chunkNum, err := strconv.Atoi(args[i])
			if err != nil {
				return fmt.Errorf("%s", i18n.T("context.io.error.invalid_chunk_number", args[i]))
			}
			selectedChunks = append(selectedChunks, chunkNum)
			i++

		case arg == "--chunks" || arg == "-C":
			if i+1 >= len(args) {
				return fmt.Errorf("%s", i18n.T("context.io.error.chunks_requires_numbers"))
			}
			i++
			// Parse "1,2,3"
			parts := strings.Split(args[i], ",")
			for _, part := range parts {
				chunkNum, err := strconv.Atoi(strings.TrimSpace(part))
				if err != nil {
					return fmt.Errorf("%s", i18n.T("context.io.error.invalid_chunk_number", part))
				}
				selectedChunks = append(selectedChunks, chunkNum)
			}
			i++

		default:
			return fmt.Errorf("%s", i18n.T("context.io.error.unknown_flag", arg))
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
			return fmt.Errorf("%s", i18n.T("context.io.error.not_chunked", contextName))
		}

		// Validar que os chunks existem
		for _, chunkNum := range selectedChunks {
			if chunkNum < 1 || chunkNum > len(ctx.Chunks) {
				return fmt.Errorf("%s", i18n.T("context.inspect.error.chunk_not_found", chunkNum, len(ctx.Chunks)))
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
	fmt.Println(colorize(i18n.T("context.io.attach_success", ctx.Name), ColorGreen))
	fmt.Printf("  %s %d\n", colorize(i18n.T("context.io.label.priority"), ColorCyan), priority)

	if len(selectedChunks) > 0 {
		fmt.Printf("  %s %v %s %d\n",
			colorize(i18n.T("context.io.label.selected_chunks"), ColorCyan),
			selectedChunks,
			i18n.T("context.io.label.of"),
			len(ctx.Chunks))

		// Mostrar detalhes dos chunks selecionados
		var totalFiles int
		var totalSize int64
		for _, chunkNum := range selectedChunks {
			chunk := ctx.Chunks[chunkNum-1] // índice base-0
			totalFiles += len(chunk.Files)
			totalSize += chunk.TotalSize
			fmt.Printf("    📦 %s\n",
				i18n.T("context.io.chunk_detail", chunkNum, chunk.Description, len(chunk.Files), float64(chunk.TotalSize)/1024))
		}
		fmt.Printf("  %s %s\n",
			colorize(i18n.T("context.io.label.total_attached"), ColorCyan),
			i18n.T("context.io.total_attached_value", totalFiles, float64(totalSize)/1024/1024))
	} else {
		if ctx.IsChunked {
			fmt.Printf("  %s %s\n",
				colorize(i18n.T("context.io.label.attached"), ColorCyan),
				i18n.T("context.io.attached_all_chunks", len(ctx.Chunks), ctx.FileCount, float64(ctx.TotalSize)/1024/1024))
		} else {
			fmt.Printf("  %s %s\n",
				colorize(i18n.T("context.io.label.attached"), ColorCyan),
				i18n.T("context.io.total_attached_value", ctx.FileCount, float64(ctx.TotalSize)/1024/1024))
		}
	}

	// Token cost feedback
	estimatedTokens := ctx.TotalSize / 4
	fmt.Printf("  %s ~%s tokens/turno %s\n",
		colorize(i18n.T("context.io.label.estimated_cost"), ColorGray),
		formatTokenCount(estimatedTokens),
		i18n.T("context.io.cached_via_system_prompt"))
	if estimatedTokens > 20000 {
		fmt.Printf("  %s %s\n",
			colorize("⚠", ColorYellow),
			i18n.T("context.io.large_context_tip"))
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
