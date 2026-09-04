/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package ctxmgr

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/diillson/chatcli/pkg/flock"
	"os"
	"path/filepath"
	"time"

	"github.com/diillson/chatcli/pkg/atrest"
	"github.com/diillson/chatcli/utils"
	"go.uber.org/zap"
)

// errContextCorrupt marks a context file that exists but cannot be parsed —
// the signal LoadAllContexts uses to quarantine it instead of skipping it
// silently on every start.
var errContextCorrupt = errors.New("context file corrupt")

// atomicWrite persists data via a same-directory temp file, fsync and an
// atomic rename (utils.AtomicWriteFile). Context files embed the whole corpus
// (multi-MB for knowledge bases); a plain WriteFile interrupted mid-write
// leaves a torn file and the knowledge base silently vanishes from the next
// load, and a rename without fsync can survive a crash while the bytes it
// points to do not.
func atomicWrite(path string, data []byte) error {
	sealed, err := atrest.Seal(data)
	if err != nil {
		return err
	}
	return utils.AtomicWriteFile(path, sealed, 0o600)
}

// atomicWritePlain is atomicWrite without the seal, for exports meant to
// leave the machine readable.
func atomicWritePlain(path string, data []byte) error {
	return utils.AtomicWriteFile(path, data, 0o600)
}

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

	// Compact encoding on purpose: a knowledge context embeds its whole corpus
	// and nobody reads a 50k-chunk JSON by eye — indentation only doubled the
	// file and the marshal allocation.
	data, err := json.Marshal(ctx)
	if err != nil {
		return fmt.Errorf("erro ao serializar contexto: %w", err)
	}

	// Cross-process: the REPL, the gateway and the MCP server share the
	// context store; last-writer-wins used to drop one side's changes.
	unlock := flock.Lock(filePath)
	err = atomicWrite(filePath, data)
	unlock()
	if err != nil {
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

	data, err := os.ReadFile(filePath) //#nosec G304 -- path supplied by user/agent through validated tool surface (boundary check upstream)
	if err == nil {
		data, err = atrest.Open(data)
	}
	if err != nil {
		return nil, fmt.Errorf("erro ao ler contexto: %w", err)
	}

	var ctx FileContext
	if err := json.Unmarshal(data, &ctx); err != nil {
		return nil, fmt.Errorf("erro ao desserializar contexto %s: %w (%w)", contextID, errContextCorrupt, err)
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
			// Parse failure: quarantine with a visible name instead of skipping
			// silently forever — a knowledge base that "disappears" from
			// /context list with only a debug-level trail is undiagnosable.
			if errors.Is(err, errContextCorrupt) {
				src := s.getContextPath(contextID)
				dst := src + ".corrupt"
				if _, serr := os.Stat(dst); serr == nil {
					dst = fmt.Sprintf("%s.corrupt-%d", src, time.Now().Unix())
				}
				if renameErr := os.Rename(src, dst); renameErr == nil {
					s.logger.Warn("Contexto corrompido movido para quarentena",
						zap.String("id", contextID),
						zap.String("quarantine", dst),
						zap.Error(err))
					continue
				}
			}
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
	// Exports keep the indented form: they are the one artifact a human may
	// open (sharing/reviewing a context definition).
	data, err := json.MarshalIndent(ctx, "", "  ")
	if err != nil {
		return fmt.Errorf("erro ao serializar contexto para exportação: %w", err)
	}

	if err := atomicWritePlain(targetPath, data); err != nil {
		return fmt.Errorf("erro ao exportar contexto: %w", err)
	}

	s.logger.Info("Contexto exportado",
		zap.String("id", ctx.ID),
		zap.String("target_path", targetPath))

	return nil
}

// ImportContext importa um contexto de um arquivo
func (s *Storage) ImportContext(sourcePath string) (*FileContext, error) {
	data, err := os.ReadFile(sourcePath) //#nosec G304 -- path supplied by user/agent through validated tool surface (boundary check upstream)
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
