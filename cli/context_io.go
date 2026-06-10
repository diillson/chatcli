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

	flags, err := parseAttachFlags(args)
	if err != nil {
		return err
	}

	// --rag selects passages semantically; --chunk(s) selects them manually.
	// Combining them is contradictory, so reject it early with a clear message.
	if flags.retrievalTopK > 0 && len(flags.selectedChunks) > 0 {
		return fmt.Errorf("%s", i18n.T("context.io.error.rag_with_chunks"))
	}

	ctx, err := h.manager.GetContextByName(contextName)
	if err != nil {
		return fmt.Errorf("%s", i18n.T("context.attach.error.not_found", contextName))
	}

	if err := validateSelectedChunks(ctx, flags.selectedChunks); err != nil {
		return err
	}

	attachOpts := ctxmgr.AttachOptions{
		Priority:       flags.priority,
		SelectedChunks: flags.selectedChunks,
		RetrievalTopK:  flags.retrievalTopK,
	}
	if err := h.manager.AttachContextWithOptions(sessionID, ctx.ID, attachOpts); err != nil {
		return fmt.Errorf("%s", i18n.T("context.attach.error.failed", err))
	}

	h.printAttachFeedback(ctx, flags)
	return nil
}

// attachFlags holds the parsed options of `/context attach <name> [flags]`.
type attachFlags struct {
	priority       int
	selectedChunks []int
	retrievalTopK  int
}

// parseAttachFlags parses the flag tail of `/context attach`. Extracted from
// handleAttach so each piece stays well within the complexity budget.
func parseAttachFlags(args []string) (attachFlags, error) {
	f := attachFlags{priority: 100}
	for i := 1; i < len(args); i++ {
		switch arg := args[i]; {
		case arg == "--priority" || arg == "-p":
			if i+1 >= len(args) {
				return f, fmt.Errorf("%s", i18n.T("context.attach.error.invalid_priority"))
			}
			i++
			p, err := strconv.Atoi(args[i])
			if err != nil {
				return f, fmt.Errorf("%s", i18n.T("context.attach.error.invalid_priority"))
			}
			f.priority = p

		case arg == "--chunk" || arg == "-c":
			if i+1 >= len(args) {
				return f, fmt.Errorf("%s", i18n.T("context.io.error.chunk_requires_number"))
			}
			i++
			chunkNum, err := strconv.Atoi(args[i])
			if err != nil {
				return f, fmt.Errorf("%s", i18n.T("context.io.error.invalid_chunk_number", args[i]))
			}
			f.selectedChunks = append(f.selectedChunks, chunkNum)

		case arg == "--chunks" || arg == "-C":
			if i+1 >= len(args) {
				return f, fmt.Errorf("%s", i18n.T("context.io.error.chunks_requires_numbers"))
			}
			i++
			nums, err := parseChunkList(args[i])
			if err != nil {
				return f, err
			}
			f.selectedChunks = append(f.selectedChunks, nums...)

		case arg == "--rag" || arg == "--retrieve" || arg == "-r":
			// Semantic retrieval: inject only the top-K relevant passages per
			// turn. An optional trailing number overrides the default K.
			f.retrievalTopK = ctxmgr.DefaultRetrievalTopK
			if i+1 < len(args) {
				if n, err := strconv.Atoi(args[i+1]); err == nil {
					if n > 0 {
						f.retrievalTopK = n
					}
					i++
				}
			}

		default:
			return f, fmt.Errorf("%s", i18n.T("context.io.error.unknown_flag", arg))
		}
	}
	return f, nil
}

// parseChunkList parses a "1,2,3" chunk specifier into a slice of numbers.
func parseChunkList(spec string) ([]int, error) {
	parts := strings.Split(spec, ",")
	out := make([]int, 0, len(parts))
	for _, part := range parts {
		n, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil {
			return nil, fmt.Errorf("%s", i18n.T("context.io.error.invalid_chunk_number", part))
		}
		out = append(out, n)
	}
	return out, nil
}

// validateSelectedChunks checks that any manually selected chunks exist on a
// chunked context.
func validateSelectedChunks(ctx *ctxmgr.FileContext, selected []int) error {
	if len(selected) == 0 {
		return nil
	}
	if !ctx.IsChunked {
		return fmt.Errorf("%s", i18n.T("context.io.error.not_chunked", ctx.Name))
	}
	for _, chunkNum := range selected {
		if chunkNum < 1 || chunkNum > len(ctx.Chunks) {
			return fmt.Errorf("%s", i18n.T("context.inspect.error.chunk_not_found", chunkNum, len(ctx.Chunks)))
		}
	}
	return nil
}

// printAttachFeedback prints the post-attach summary: priority, retrieval mode,
// chunk/whole-content breakdown and estimated token cost.
func (h *ContextHandler) printAttachFeedback(ctx *ctxmgr.FileContext, flags attachFlags) {
	fmt.Println(colorize(i18n.T("context.io.attach_success", ctx.Name), ColorGreen))
	fmt.Printf("  %s %d\n", colorize(i18n.T("context.io.label.priority"), ColorCyan), flags.priority)

	if flags.retrievalTopK > 0 {
		if h.manager.RetrievalEnabled() {
			fmt.Printf("  %s %d\n",
				colorize(i18n.T("context.io.label.rag_topk"), ColorCyan), flags.retrievalTopK)
		} else {
			// Provider absent: the attachment still works, but as whole content.
			fmt.Println(colorize(i18n.T("context.io.warn.rag_no_provider"), ColorYellow))
		}
	}

	if ctx.Mode == ctxmgr.ModeKnowledge {
		printKnowledgeAttachFeedback(ctx)
		return
	}

	if len(flags.selectedChunks) > 0 {
		printSelectedChunksFeedback(ctx, flags.selectedChunks)
	} else {
		printWholeContextFeedback(ctx)
	}
	printTokenCostFeedback(ctx)
}

// printKnowledgeAttachFeedback reports the index-card economics of a knowledge
// attachment: the corpus stays out of the prompt, only the digest plus the
// per-turn retrieved passages are paid for.
func printKnowledgeAttachFeedback(ctx *ctxmgr.FileContext) {
	digestTokens := int64(len(ctxmgr.BuildKnowledgeDigest(ctx, 0))) / 4
	fmt.Printf("  %s %s\n",
		colorize(i18n.T("context.io.label.attached"), ColorCyan),
		i18n.T("context.io.knowledge_attached", ctx.FileCount, float64(ctx.TotalSize)/1024/1024))
	fmt.Printf("  %s ~%s tokens/turno %s\n",
		colorize(i18n.T("context.io.label.estimated_cost"), ColorGray),
		formatTokenCount(digestTokens),
		i18n.T("context.io.knowledge_cost_note"))
}

func printSelectedChunksFeedback(ctx *ctxmgr.FileContext, selected []int) {
	fmt.Printf("  %s %v %s %d\n",
		colorize(i18n.T("context.io.label.selected_chunks"), ColorCyan),
		selected,
		i18n.T("context.io.label.of"),
		len(ctx.Chunks))

	var totalFiles int
	var totalSize int64
	for _, chunkNum := range selected {
		chunk := ctx.Chunks[chunkNum-1] // índice base-0
		totalFiles += len(chunk.Files)
		totalSize += chunk.TotalSize
		fmt.Printf("    📦 %s\n",
			i18n.T("context.io.chunk_detail", chunkNum, chunk.Description, len(chunk.Files), float64(chunk.TotalSize)/1024))
	}
	fmt.Printf("  %s %s\n",
		colorize(i18n.T("context.io.label.total_attached"), ColorCyan),
		i18n.T("context.io.total_attached_value", totalFiles, float64(totalSize)/1024/1024))
}

func printWholeContextFeedback(ctx *ctxmgr.FileContext) {
	if ctx.IsChunked {
		fmt.Printf("  %s %s\n",
			colorize(i18n.T("context.io.label.attached"), ColorCyan),
			i18n.T("context.io.attached_all_chunks", len(ctx.Chunks), ctx.FileCount, float64(ctx.TotalSize)/1024/1024))
		return
	}
	fmt.Printf("  %s %s\n",
		colorize(i18n.T("context.io.label.attached"), ColorCyan),
		i18n.T("context.io.total_attached_value", ctx.FileCount, float64(ctx.TotalSize)/1024/1024))
}

func printTokenCostFeedback(ctx *ctxmgr.FileContext) {
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
