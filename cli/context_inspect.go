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
	"strconv"
	"strings"

	"github.com/diillson/chatcli/cli/ctxmgr"
	"github.com/diillson/chatcli/i18n"
)

// handleInspect fornece inspeção profunda de um contexto
func (h *ContextHandler) handleInspect(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("%s", i18n.T("context.inspect.usage"))
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
		return fmt.Errorf("%s", i18n.T("context.inspect.error.not_found", contextName))
	}

	// Se chunk específico foi solicitado
	if chunkNum > 0 && ctx.IsChunked {
		if chunkNum > len(ctx.Chunks) {
			return fmt.Errorf("%s", i18n.T("context.inspect.error.chunk_not_found", chunkNum, len(ctx.Chunks)))
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
	fmt.Println(colorize(i18n.T("context.inspect.header"), ColorLime+ColorBold))
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
			sizeClass = i18n.T("context.inspect.size_class.small")
		case sizeKB < 100:
			sizeClass = i18n.T("context.inspect.size_class.medium")
		default:
			sizeClass = i18n.T("context.inspect.size_class.large")
		}
		sizeDistribution[sizeClass]++
	}

	fmt.Printf("\n%s\n", colorize(i18n.T("context.inspect.stats_header"), ColorCyan+ColorBold))
	fmt.Printf("  %s %s\n",
		i18n.T("context.inspect.total_lines"),
		colorize(fmt.Sprintf("%d", totalLines), ColorYellow))
	fmt.Printf("  %s %s\n",
		i18n.T("context.inspect.avg_lines"),
		colorize(fmt.Sprintf("%.0f", float64(totalLines)/float64(ctx.FileCount)), ColorYellow))

	fmt.Printf("\n%s\n", colorize(i18n.T("context.inspect.size_distribution_header"), ColorCyan+ColorBold))
	for size, count := range sizeDistribution {
		percentage := float64(count) / float64(ctx.FileCount) * 100
		fmt.Printf("  %s\n", i18n.T("context.inspect.size_distribution_line", size, count, percentage))
	}

	fmt.Printf("\n%s\n", colorize(i18n.T("context.inspect.extensions_header"), ColorCyan+ColorBold))
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
			ext = i18n.T("context.inspect.no_extension")
		}
		fmt.Printf("  %s\n", i18n.T("context.inspect.extension_line", ext, count))
	}

	// Análise de chunks se aplicável
	if ctx.IsChunked {
		fmt.Printf("\n%s\n", colorize(i18n.T("context.inspect.chunk_analysis_header"), ColorCyan+ColorBold))

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

		fmt.Printf("  %s %.2f KB\n", i18n.T("context.inspect.chunk_avg_size"), float64(avgSize)/1024)
		fmt.Printf("  %s %.2f KB\n", i18n.T("context.inspect.chunk_min_size"), float64(minSize)/1024)
		fmt.Printf("  %s %.2f KB\n", i18n.T("context.inspect.chunk_max_size"), float64(maxSize)/1024)
		fmt.Printf("  %s %.1f%%\n",
			i18n.T("context.inspect.chunk_variation"),
			float64(maxSize-minSize)/float64(avgSize)*100)
	}

	fmt.Println()
}

// inspectChunk mostra detalhes de um chunk específico
func (h *ContextHandler) inspectChunk(ctx *ctxmgr.FileContext, index int) {
	chunk := ctx.Chunks[index]

	fmt.Printf("\n%s\n", i18n.T("context.inspect.chunk_header", chunk.Index, chunk.TotalChunks))
	fmt.Println(colorize(strings.Repeat("═", 80), ColorGray))

	fmt.Printf("\n%s %s\n", colorize(i18n.T("context.display.label.description"), ColorCyan), chunk.Description)
	fmt.Printf("%s %s\n", colorize(i18n.T("context.display.label.files"), ColorCyan), i18n.T("context.inspect.files_count", len(chunk.Files)))
	fmt.Printf("%s %.2f KB\n", colorize(i18n.T("context.display.label.size"), ColorCyan), float64(chunk.TotalSize)/1024)
	fmt.Printf("%s ~%d tokens\n", colorize(i18n.T("context.inspect.estimated_tokens"), ColorCyan), chunk.EstTokens)

	fmt.Printf("\n%s\n", colorize(i18n.T("context.inspect.file_list_header"), ColorCyan+ColorBold))

	for i, file := range chunk.Files {
		lines := strings.Count(file.Content, "\n") + 1
		fmt.Printf("  %d. %s\n", i+1, colorize(file.Path, ColorYellow))
		fmt.Printf("     %s %s | %s %d | %s %.2f KB\n",
			colorize(i18n.T("context.inspect.label.type"), ColorGray), file.Type,
			colorize(i18n.T("context.inspect.label.lines"), ColorGray), lines,
			colorize(i18n.T("context.display.label.size"), ColorGray), float64(file.Size)/1024)
	}

	fmt.Println()
}
