/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
 */
package ctxmgr

import (
	"fmt"
	"strings"

	"github.com/diillson/chatcli/utils"
	"go.uber.org/zap"
)

// Processor processa arquivos e diretórios para contextos
type Processor struct {
	logger *zap.Logger
}

// NewProcessor cria uma nova instância de Processor
func NewProcessor(logger *zap.Logger) *Processor {
	return &Processor{
		logger: logger,
	}
}

// ProcessPaths processa múltiplos caminhos baseado no modo
func (p *Processor) ProcessPaths(paths []string, mode ProcessingMode) ([]utils.FileInfo, utils.DirectoryScanOptions, error) {
	if len(paths) == 0 {
		return nil, utils.DirectoryScanOptions{}, fmt.Errorf("nenhum caminho fornecido")
	}

	scanOpts := utils.DefaultDirectoryScanOptions(p.logger)

	// Ajustar opções baseado no modo
	switch mode {
	case ModeFull:
		scanOpts.MaxTotalSize = 50 * 1024 * 1024 // 50MB
		scanOpts.MaxFilesToProcess = 500
		p.logger.Debug("Modo Full configurado",
			zap.Int64("max_size", scanOpts.MaxTotalSize),
			zap.Int("max_files", scanOpts.MaxFilesToProcess))

	case ModeSummary:
		// Apenas estrutura, sem conteúdo completo
		scanOpts.MaxTotalSize = 5 * 1024 * 1024 // 5MB (metadados + previews)
		scanOpts.MaxFilesToProcess = 1000
		p.logger.Debug("Modo Summary configurado",
			zap.Int64("max_size", scanOpts.MaxTotalSize),
			zap.Int("max_files", scanOpts.MaxFilesToProcess))

	case ModeChunked:
		scanOpts.MaxTotalSize = 100 * 1024 * 1024 // 100MB
		scanOpts.MaxFilesToProcess = 1000
		p.logger.Debug("Modo Chunked configurado",
			zap.Int64("max_size", scanOpts.MaxTotalSize),
			zap.Int("max_files", scanOpts.MaxFilesToProcess))

	case ModeSmart:
		scanOpts.MaxTotalSize = 20 * 1024 * 1024 // 20MB
		scanOpts.MaxFilesToProcess = 200
		p.logger.Debug("Modo Smart configurado",
			zap.Int64("max_size", scanOpts.MaxTotalSize),
			zap.Int("max_files", scanOpts.MaxFilesToProcess))

	default:
		return nil, scanOpts, fmt.Errorf("modo de processamento inválido: %s", mode)
	}

	allFiles := make([]utils.FileInfo, 0)
	processedPaths := make(map[string]bool) // Para evitar duplicatas

	for _, path := range paths {
		// Expandir path
		expandedPath, err := utils.ExpandPath(path)
		if err != nil {
			p.logger.Warn("Erro ao expandir caminho, usando original",
				zap.String("path", path),
				zap.Error(err))
			expandedPath = path
		}

		// Verificar duplicata
		if processedPaths[expandedPath] {
			p.logger.Debug("Caminho duplicado, pulando",
				zap.String("path", expandedPath))
			continue
		}
		processedPaths[expandedPath] = true

		p.logger.Debug("Processando caminho",
			zap.String("path", expandedPath),
			zap.String("mode", string(mode)))

		files, err := utils.ProcessDirectory(expandedPath, scanOpts)
		if err != nil {
			return nil, scanOpts, fmt.Errorf("erro ao processar '%s': %w", expandedPath, err)
		}

		p.logger.Debug("Caminho processado",
			zap.String("path", expandedPath),
			zap.Int("files_found", len(files)))

		allFiles = append(allFiles, files...)
	}

	// Aplicar pós-processamento baseado no modo
	allFiles, err := p.postProcess(allFiles, mode)
	if err != nil {
		return nil, scanOpts, fmt.Errorf("erro no pós-processamento: %w", err)
	}

	p.logger.Info("Caminhos processados com sucesso",
		zap.Int("total_files", len(allFiles)),
		zap.String("mode", string(mode)),
		zap.Int("paths_processed", len(paths)))

	return allFiles, scanOpts, nil
}

// postProcess aplica transformações pós-processamento baseado no modo
func (p *Processor) postProcess(files []utils.FileInfo, mode ProcessingMode) ([]utils.FileInfo, error) {
	switch mode {
	case ModeSummary:
		// Para summary, truncar conteúdo dos arquivos (manter apenas primeiras linhas)
		return p.createSummaryView(files), nil

	case ModeChunked:
		// Para chunked, arquivos já processados normalmente
		// O chunking será feito na hora de enviar à LLM
		return files, nil

	case ModeSmart:
		// Para smart, arquivos já foram filtrados pelo utils.ProcessDirectory
		return files, nil

	default:
		// Full e outros modos retornam arquivos sem modificação
		return files, nil
	}
}

// createSummaryView cria uma visualização resumida dos arquivos
func (p *Processor) createSummaryView(files []utils.FileInfo) []utils.FileInfo {
	summaryFiles := make([]utils.FileInfo, len(files))

	for i, file := range files {
		// Para summary, manter apenas metadados e primeiras linhas
		const maxPreviewLines = 10
		const maxPreviewChars = 500

		lines := strings.Split(file.Content, "\n")
		preview := ""

		if len(lines) > maxPreviewLines {
			preview = strings.Join(lines[:maxPreviewLines], "\n")
			preview += fmt.Sprintf("\n... (%d linhas omitidas) ...", len(lines)-maxPreviewLines)
		} else {
			preview = file.Content
		}

		if len(preview) > maxPreviewChars {
			preview = preview[:maxPreviewChars] + "..."
		}

		summaryFiles[i] = utils.FileInfo{
			Path:    file.Path,
			Content: preview,
			Size:    file.Size,
			Type:    file.Type,
		}
	}

	p.logger.Debug("Visualização de summary criada",
		zap.Int("files", len(summaryFiles)))

	return summaryFiles
}

// EstimateTokenCount estima o número de tokens em um conjunto de arquivos
func (p *Processor) EstimateTokenCount(files []utils.FileInfo) int {
	// Estimativa conservadora: ~4 caracteres por token
	var totalChars int
	for _, file := range files {
		totalChars += len(file.Content)
	}

	estimatedTokens := totalChars / 4

	p.logger.Debug("Tokens estimados",
		zap.Int("total_chars", totalChars),
		zap.Int("estimated_tokens", estimatedTokens))

	return estimatedTokens
}
