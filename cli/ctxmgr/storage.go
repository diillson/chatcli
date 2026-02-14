/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
 */
package ctxmgr

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/diillson/chatcli/utils"
	"go.uber.org/zap"
)

// Storage gerencia a persistência de contextos em disco
type Storage struct {
	basePath string
	logger   *zap.Logger
}

// NewStorage cria uma nova instância de Storage
func NewStorage(logger *zap.Logger) (*Storage, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("erro ao obter diretório home: %w", err)
	}

	basePath := filepath.Join(homeDir, ".chatcli", "contexts")
	if err := os.MkdirAll(basePath, 0o700); err != nil {
		return nil, fmt.Errorf("erro ao criar diretório de contextos: %w", err)
	}

	return &Storage{
		basePath: basePath,
		logger:   logger,
	}, nil
}

// SaveContext salva um contexto em disco
func (s *Storage) SaveContext(ctx *FileContext) error {
	filePath := s.getContextPath(ctx.ID)

	data, err := json.MarshalIndent(ctx, "", "  ")
	if err != nil {
		return fmt.Errorf("erro ao serializar contexto: %w", err)
	}

	if err := os.WriteFile(filePath, data, 0o600); err != nil {
		return fmt.Errorf("erro ao salvar contexto: %w", err)
	}

	s.logger.Debug("Contexto salvo no disco",
		zap.String("id", ctx.ID),
		zap.String("path", filePath))

	return nil
}

// LoadContext carrega um contexto do disco
func (s *Storage) LoadContext(contextID string) (*FileContext, error) {
	filePath := s.getContextPath(contextID)

	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("erro ao ler contexto: %w", err)
	}

	var ctx FileContext
	if err := json.Unmarshal(data, &ctx); err != nil {
		return nil, fmt.Errorf("erro ao desserializar contexto: %w", err)
	}

	// NOVO: Reconstruir ScanOptions a partir dos metadados
	ctx.ScanOptions = utils.DirectoryScanOptions{
		MaxTotalSize:      ctx.ScanOptionsMetadata.MaxTotalSize,
		MaxFilesToProcess: ctx.ScanOptionsMetadata.MaxFilesToProcess,
		Extensions:        ctx.ScanOptionsMetadata.Extensions,
		ExcludeDirs:       ctx.ScanOptionsMetadata.ExcludeDirs,
		ExcludePatterns:   ctx.ScanOptionsMetadata.ExcludePatterns,
		IncludeHidden:     ctx.ScanOptionsMetadata.IncludeHidden,
		Logger:            s.logger,
		OnFileProcessed:   nil, // Callback não é restaurado (não é necessário após load)
	}

	return &ctx, nil
}

// LoadAllContexts carrega todos os contextos do disco
func (s *Storage) LoadAllContexts() ([]*FileContext, error) {
	entries, err := os.ReadDir(s.basePath)
	if err != nil {
		return nil, fmt.Errorf("erro ao ler diretório de contextos: %w", err)
	}

	contexts := make([]*FileContext, 0)

	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}

		contextID := entry.Name()[:len(entry.Name())-5] // Remove .json
		ctx, err := s.LoadContext(contextID)
		if err != nil {
			s.logger.Warn("Erro ao carregar contexto, pulando",
				zap.String("id", contextID),
				zap.Error(err))
			continue
		}

		contexts = append(contexts, ctx)
	}

	return contexts, nil
}

// DeleteContext deleta um contexto do disco
func (s *Storage) DeleteContext(contextID string) error {
	filePath := s.getContextPath(contextID)

	if err := os.Remove(filePath); err != nil {
		return fmt.Errorf("erro ao deletar contexto: %w", err)
	}

	s.logger.Debug("Contexto deletado do disco",
		zap.String("id", contextID),
		zap.String("path", filePath))

	return nil
}

// GetStoragePath retorna o caminho base de armazenamento
func (s *Storage) GetStoragePath() string {
	return s.basePath
}

// ExportContext exporta um contexto para um arquivo específico
func (s *Storage) ExportContext(ctx *FileContext, targetPath string) error {
	data, err := json.MarshalIndent(ctx, "", "  ")
	if err != nil {
		return fmt.Errorf("erro ao serializar contexto para exportação: %w", err)
	}

	if err := os.WriteFile(targetPath, data, 0o600); err != nil {
		return fmt.Errorf("erro ao exportar contexto: %w", err)
	}

	s.logger.Info("Contexto exportado",
		zap.String("id", ctx.ID),
		zap.String("target_path", targetPath))

	return nil
}

// ImportContext importa um contexto de um arquivo
func (s *Storage) ImportContext(sourcePath string) (*FileContext, error) {
	data, err := os.ReadFile(sourcePath)
	if err != nil {
		return nil, fmt.Errorf("erro ao ler arquivo de importação: %w", err)
	}

	var ctx FileContext
	if err := json.Unmarshal(data, &ctx); err != nil {
		return nil, fmt.Errorf("erro ao desserializar contexto importado: %w", err)
	}

	// Salvar no storage padrão
	if err := s.SaveContext(&ctx); err != nil {
		return nil, fmt.Errorf("erro ao salvar contexto importado: %w", err)
	}

	s.logger.Info("Contexto importado",
		zap.String("id", ctx.ID),
		zap.String("source_path", sourcePath))

	return &ctx, nil
}

// getContextPath retorna o caminho completo do arquivo de contexto.
// Validates the contextID to prevent path traversal attacks.
func (s *Storage) getContextPath(contextID string) string {
	// Sanitize: use only the base name to prevent directory traversal
	safe := filepath.Base(contextID)
	return filepath.Join(s.basePath, safe+".json")
}
