/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
 */
package ctxmgr

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/diillson/chatcli/utils"
	"go.uber.org/zap"
)

const (
	// Tamanho alvo por chunk (em tokens estimados)
	DefaultChunkTargetTokens = 30000 // ~120KB de texto

	// Tamanho máximo por chunk
	MaxChunkTokens = 50000 // ~200KB de texto

	// Tamanho mínimo para considerar dividir
	MinFilesForChunking = 10
)

// Chunker divide arquivos em chunks inteligentes
type Chunker struct {
	logger       *zap.Logger
	targetTokens int
	maxTokens    int
}

// NewChunker cria uma nova instância de Chunker
func NewChunker(logger *zap.Logger) *Chunker {
	return &Chunker{
		logger:       logger,
		targetTokens: DefaultChunkTargetTokens,
		maxTokens:    MaxChunkTokens,
	}
}

// ChunkStrategy define a estratégia de divisão
type ChunkStrategy string

const (
	ChunkByDirectory ChunkStrategy = "directory" // Agrupar por diretório
	ChunkByFileType  ChunkStrategy = "filetype"  // Agrupar por tipo de arquivo
	ChunkBySize      ChunkStrategy = "size"      // Dividir por tamanho
	ChunkSmart       ChunkStrategy = "smart"     // Estratégia inteligente híbrida
)

// FileChunk representa um chunk de arquivos
type FileChunk struct {
	Index       int              `json:"index"`        // Índice do chunk (1-based)
	TotalChunks int              `json:"total_chunks"` // Total de chunks
	Files       []utils.FileInfo `json:"files"`        // Arquivos neste chunk
	Description string           `json:"description"`  // Descrição do chunk
	TotalSize   int64            `json:"total_size"`   // Tamanho total
	EstTokens   int              `json:"est_tokens"`   // Tokens estimados
}

// DivideIntoChunks divide arquivos em chunks usando estratégia inteligente
func (c *Chunker) DivideIntoChunks(files []utils.FileInfo, strategy ChunkStrategy) ([]FileChunk, error) {
	if len(files) == 0 {
		return nil, nil
	}

	// Se poucos arquivos, não dividir
	if len(files) < MinFilesForChunking {
		c.logger.Debug("Poucos arquivos, não dividindo em chunks",
			zap.Int("file_count", len(files)))
		return []FileChunk{{
			Index:       1,
			TotalChunks: 1,
			Files:       files,
			Description: "Chunk único (projeto pequeno)",
			TotalSize:   c.calculateTotalSize(files),
			EstTokens:   c.estimateTokens(files),
		}}, nil
	}

	var chunks []FileChunk
	var err error

	switch strategy {
	case ChunkByDirectory:
		chunks = c.chunkByDirectory(files)

	case ChunkByFileType:
		chunks = c.chunkByFileType(files)

	case ChunkBySize:
		chunks = c.chunkBySize(files)

	case ChunkSmart:
		chunks = c.chunkSmart(files)

	default:
		chunks = c.chunkSmart(files)
	}

	// Atualizar índices e totais
	totalChunks := len(chunks)
	for i := range chunks {
		chunks[i].Index = i + 1
		chunks[i].TotalChunks = totalChunks
	}

	c.logger.Info("Arquivos divididos em chunks",
		zap.Int("total_files", len(files)),
		zap.Int("total_chunks", totalChunks),
		zap.String("strategy", string(strategy)))

	return chunks, err
}

// chunkByDirectory agrupa arquivos por diretório
func (c *Chunker) chunkByDirectory(files []utils.FileInfo) []FileChunk {
	// Agrupar por diretório
	dirGroups := make(map[string][]utils.FileInfo)

	for _, file := range files {
		dir := filepath.Dir(file.Path)
		dirGroups[dir] = append(dirGroups[dir], file)
	}

	// Ordenar diretórios
	var dirs []string
	for dir := range dirGroups {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)

	var chunks []FileChunk
	var currentChunk FileChunk
	currentTokens := 0

	for _, dir := range dirs {
		dirFiles := dirGroups[dir]
		dirTokens := c.estimateTokens(dirFiles)

		// Se adicionar este diretório exceder o limite, finalizar chunk atual
		if currentTokens > 0 && currentTokens+dirTokens > c.targetTokens {
			currentChunk.Description = fmt.Sprintf("Chunk com %d diretórios",
				len(c.getUniqueDirs(currentChunk.Files)))
			currentChunk.TotalSize = c.calculateTotalSize(currentChunk.Files)
			currentChunk.EstTokens = currentTokens
			chunks = append(chunks, currentChunk)

			// Resetar para novo chunk
			currentChunk = FileChunk{Files: []utils.FileInfo{}}
			currentTokens = 0
		}

		// Adicionar arquivos do diretório ao chunk atual
		currentChunk.Files = append(currentChunk.Files, dirFiles...)
		currentTokens += dirTokens
	}

	// Adicionar último chunk
	if len(currentChunk.Files) > 0 {
		currentChunk.Description = fmt.Sprintf("Chunk com %d diretórios",
			len(c.getUniqueDirs(currentChunk.Files)))
		currentChunk.TotalSize = c.calculateTotalSize(currentChunk.Files)
		currentChunk.EstTokens = currentTokens
		chunks = append(chunks, currentChunk)
	}

	return chunks
}

// chunkByFileType agrupa arquivos por tipo
func (c *Chunker) chunkByFileType(files []utils.FileInfo) []FileChunk {
	// Agrupar por tipo
	typeGroups := make(map[string][]utils.FileInfo)

	for _, file := range files {
		fileType := file.Type
		if fileType == "" {
			fileType = "Other"
		}
		typeGroups[fileType] = append(typeGroups[fileType], file)
	}

	// Ordenar tipos
	var types []string
	for t := range typeGroups {
		types = append(types, t)
	}
	sort.Strings(types)

	var chunks []FileChunk
	var currentChunk FileChunk
	currentTokens := 0
	currentTypes := make(map[string]bool)

	for _, fileType := range types {
		typeFiles := typeGroups[fileType]
		typeTokens := c.estimateTokens(typeFiles)

		// Se adicionar este tipo exceder o limite, finalizar chunk atual
		if currentTokens > 0 && currentTokens+typeTokens > c.targetTokens {
			currentChunk.Description = fmt.Sprintf("Chunk com tipos: %s",
				strings.Join(c.getKeys(currentTypes), ", "))
			currentChunk.TotalSize = c.calculateTotalSize(currentChunk.Files)
			currentChunk.EstTokens = currentTokens
			chunks = append(chunks, currentChunk)

			// Resetar para novo chunk
			currentChunk = FileChunk{Files: []utils.FileInfo{}}
			currentTokens = 0
			currentTypes = make(map[string]bool)
		}

		// Adicionar arquivos do tipo ao chunk atual
		currentChunk.Files = append(currentChunk.Files, typeFiles...)
		currentTokens += typeTokens
		currentTypes[fileType] = true
	}

	// Adicionar último chunk
	if len(currentChunk.Files) > 0 {
		currentChunk.Description = fmt.Sprintf("Chunk com tipos: %s",
			strings.Join(c.getKeys(currentTypes), ", "))
		currentChunk.TotalSize = c.calculateTotalSize(currentChunk.Files)
		currentChunk.EstTokens = currentTokens
		chunks = append(chunks, currentChunk)
	}

	return chunks
}

// chunkBySize divide baseado apenas em tamanho/tokens
func (c *Chunker) chunkBySize(files []utils.FileInfo) []FileChunk {
	var chunks []FileChunk
	var currentChunk FileChunk
	currentTokens := 0

	for _, file := range files {
		fileTokens := c.estimateTokensForFile(file)

		// Se este arquivo sozinho excede o máximo, criar chunk separado
		if fileTokens > c.maxTokens {
			if len(currentChunk.Files) > 0 {
				currentChunk.Description = fmt.Sprintf("Chunk com %d arquivos",
					len(currentChunk.Files))
				currentChunk.TotalSize = c.calculateTotalSize(currentChunk.Files)
				currentChunk.EstTokens = currentTokens
				chunks = append(chunks, currentChunk)
				currentChunk = FileChunk{Files: []utils.FileInfo{}}
				currentTokens = 0
			}

			// Chunk separado para arquivo grande
			chunks = append(chunks, FileChunk{
				Files:       []utils.FileInfo{file},
				Description: fmt.Sprintf("Arquivo grande: %s", filepath.Base(file.Path)),
				TotalSize:   file.Size,
				EstTokens:   fileTokens,
			})
			continue
		}

		// Se adicionar exceder o alvo, finalizar chunk atual
		if currentTokens > 0 && currentTokens+fileTokens > c.targetTokens {
			currentChunk.Description = fmt.Sprintf("Chunk com %d arquivos",
				len(currentChunk.Files))
			currentChunk.TotalSize = c.calculateTotalSize(currentChunk.Files)
			currentChunk.EstTokens = currentTokens
			chunks = append(chunks, currentChunk)

			currentChunk = FileChunk{Files: []utils.FileInfo{}}
			currentTokens = 0
		}

		currentChunk.Files = append(currentChunk.Files, file)
		currentTokens += fileTokens
	}

	// Adicionar último chunk
	if len(currentChunk.Files) > 0 {
		currentChunk.Description = fmt.Sprintf("Chunk com %d arquivos",
			len(currentChunk.Files))
		currentChunk.TotalSize = c.calculateTotalSize(currentChunk.Files)
		currentChunk.EstTokens = currentTokens
		chunks = append(chunks, currentChunk)
	}

	return chunks
}

// chunkSmart usa estratégia híbrida inteligente
func (c *Chunker) chunkSmart(files []utils.FileInfo) []FileChunk {
	// Análise inicial
	dirCount := len(c.getUniqueDirs(files))
	typeCount := len(c.getUniqueTypes(files))
	totalTokens := c.estimateTokens(files)

	c.logger.Debug("Análise para chunking inteligente",
		zap.Int("total_files", len(files)),
		zap.Int("directories", dirCount),
		zap.Int("file_types", typeCount),
		zap.Int("est_tokens", totalTokens))

	// Decidir estratégia baseado na estrutura do projeto
	if dirCount > 10 && dirCount > typeCount*2 {
		// Muitos diretórios, estrutura modular
		c.logger.Debug("Usando estratégia por diretório")
		return c.chunkByDirectory(files)
	}

	if typeCount > 5 && typeCount*3 > dirCount {
		// Muitos tipos diferentes, projeto heterogêneo
		c.logger.Debug("Usando estratégia por tipo de arquivo")
		return c.chunkByFileType(files)
	}

	// Fallback para tamanho
	c.logger.Debug("Usando estratégia por tamanho")
	return c.chunkBySize(files)
}

// Helper methods

func (c *Chunker) estimateTokens(files []utils.FileInfo) int {
	total := 0
	for _, f := range files {
		total += c.estimateTokensForFile(f)
	}
	return total
}

func (c *Chunker) estimateTokensForFile(file utils.FileInfo) int {
	// Estimativa: ~4 caracteres por token
	// Adicionar overhead para formatação (~20%)
	baseTokens := len(file.Content) / 4
	overhead := int(float64(baseTokens) * 0.2)
	return baseTokens + overhead
}

func (c *Chunker) calculateTotalSize(files []utils.FileInfo) int64 {
	var total int64
	for _, f := range files {
		total += f.Size
	}
	return total
}

func (c *Chunker) getUniqueDirs(files []utils.FileInfo) map[string]bool {
	dirs := make(map[string]bool)
	for _, f := range files {
		dirs[filepath.Dir(f.Path)] = true
	}
	return dirs
}

func (c *Chunker) getUniqueTypes(files []utils.FileInfo) map[string]bool {
	types := make(map[string]bool)
	for _, f := range files {
		if f.Type != "" {
			types[f.Type] = true
		}
	}
	return types
}

func (c *Chunker) getKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
