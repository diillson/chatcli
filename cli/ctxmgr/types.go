/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
 */
package ctxmgr

import (
	"time"

	"github.com/diillson/chatcli/utils"
)

// ProcessingMode define o modo de processamento de arquivos no contexto
type ProcessingMode string

const (
	ModeFull    ProcessingMode = "full"    // Conteúdo completo
	ModeSummary ProcessingMode = "summary" // Apenas estrutura
	ModeChunked ProcessingMode = "chunked" // Dividido em chunks
	ModeSmart   ProcessingMode = "smart"   // Seleção inteligente
)

// FileContext representa um contexto gerenciado contendo arquivos e metadados
type FileContext struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Files       []utils.FileInfo  `json:"files"`
	Mode        ProcessingMode    `json:"mode"`
	TotalSize   int64             `json:"total_size"`
	FileCount   int               `json:"file_count"`
	CreatedAt   time.Time         `json:"created_at"`
	UpdatedAt   time.Time         `json:"updated_at"`
	Tags        []string          `json:"tags"`
	Metadata    map[string]string `json:"metadata"`

	ScanOptions         utils.DirectoryScanOptions `json:"-"`
	ScanOptionsMetadata ScanOptionsMetadata        `json:"scan_options_metadata"`

	Chunks        []FileChunk `json:"chunks,omitempty"`         // Chunks divididos (se modo chunked)
	IsChunked     bool        `json:"is_chunked"`               // Se foi dividido em chunks
	ChunkStrategy string      `json:"chunk_strategy,omitempty"` // Estratégia usada
}

// ScanOptionsMetadata contém versão serializável das opções de scan
type ScanOptionsMetadata struct {
	MaxTotalSize      int64    `json:"max_total_size"`
	MaxFilesToProcess int      `json:"max_files_to_process"`
	Extensions        []string `json:"extensions"`
	ExcludeDirs       []string `json:"exclude_dirs"`
	ExcludePatterns   []string `json:"exclude_patterns"`
	IncludeHidden     bool     `json:"include_hidden"`
}

// AttachedContext representa um contexto anexado a uma sessão
type AttachedContext struct {
	ContextID      string    `json:"context_id"`                // ID do contexto
	AttachedAt     time.Time `json:"attached_at"`               // Quando foi anexado
	Priority       int       `json:"priority"`                  // Prioridade na ordem de mensagens (menor = primeiro)
	SelectedChunks []int     `json:"selected_chunks,omitempty"` // CORREÇÃO: Adicionado campo para chunks selecionados
}

// ContextMetrics contém métricas sobre o uso de contextos
type ContextMetrics struct {
	TotalContexts    int            `json:"total_contexts"`
	AttachedContexts int            `json:"attached_contexts"`
	TotalFiles       int            `json:"total_files"`
	TotalSizeBytes   int64          `json:"total_size_bytes"`
	ContextsByMode   map[string]int `json:"contexts_by_mode"`
	LastUpdated      time.Time      `json:"last_updated"`
	StoragePath      string         `json:"storage_path"`
}

// MergeOptions configura como contextos devem ser mesclados
type MergeOptions struct {
	RemoveDuplicates bool     `json:"remove_duplicates"` // Remove arquivos duplicados
	SortByPath       bool     `json:"sort_by_path"`      // Ordena por caminho
	PreferNewer      bool     `json:"prefer_newer"`      // Prefere versões mais recentes em duplicatas
	Tags             []string `json:"tags"`              // Tags para o contexto mesclado
}

// ContextFilter filtra contextos ao listar
type ContextFilter struct {
	Tags          []string       `json:"tags"`           // Filtrar por tags
	Mode          ProcessingMode `json:"mode"`           // Filtrar por modo
	MinSize       int64          `json:"min_size"`       // Tamanho mínimo
	MaxSize       int64          `json:"max_size"`       // Tamanho máximo
	CreatedAfter  *time.Time     `json:"created_after"`  // Criado após
	CreatedBefore *time.Time     `json:"created_before"` // Criado antes
	NamePattern   string         `json:"name_pattern"`   // Padrão regex para nome
}

// ValidationResult resultado da validação de um contexto
type ValidationResult struct {
	Valid    bool     `json:"valid"`
	Errors   []string `json:"errors"`
	Warnings []string `json:"warnings"`
}

// FormatOptions opções para formatar contexto como prompt
type FormatOptions struct {
	IncludeMetadata  bool `json:"include_metadata"`  // Incluir metadados no prompt
	IncludeTimestamp bool `json:"include_timestamp"` // Incluir timestamp
	Compact          bool `json:"compact"`           // Formato compacto (sem índice)
}

// AttachOptions define opções para anexar contextos
type AttachOptions struct {
	Priority       int
	SelectedChunks []int // Vazio = todos os chunks
}
