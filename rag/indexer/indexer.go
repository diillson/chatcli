package indexer

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/diillson/chatcli/rag/chunker"
	"github.com/diillson/chatcli/rag/vectordb"
	"github.com/diillson/chatcli/utils"
	"go.uber.org/zap"
)

// ProjectIndexer gerencia a indexação de projetos
type ProjectIndexer struct {
	db          vectordb.VectorDB
	chunker     chunker.Chunker
	codeChunker chunker.Chunker
	logger      *zap.Logger
	workerCount int // Número de workers para processamento paralelo
}

// NewProjectIndexer cria uma nova instância do ProjectIndexer com limites razoáveis
func NewProjectIndexer(db vectordb.VectorDB, logger *zap.Logger) *ProjectIndexer {
	return &ProjectIndexer{
		db: db,
		// Chunkers com chunks menores
		chunker:     chunker.NewRecursiveChunker(500, 50), // 500 caracteres por chunk
		codeChunker: chunker.NewCodeChunker(500, 50),      // 500 caracteres por chunk
		logger:      logger,
		workerCount: 2, // Menos workers para não sobrecarregar
	}
}

// IndexProject indexa um projeto completo
func (pi *ProjectIndexer) IndexProject(ctx context.Context, projectPath string) error {
	projectPath, err := utils.ExpandPath(projectPath)
	if err != nil {
		return fmt.Errorf("erro ao expandir o caminho: %w", err)
	}

	// Usar as mesmas opções do ProcessDirectory existente
	scanOptions := utils.DefaultDirectoryScanOptions(pi.logger)

	// Remover limite de tamanho para indexação completa
	scanOptions.MaxTotalSize = 30 * 1024 * 1024 // 30MB
	scanOptions.MaxFilesToProcess = 1000        // Aumentar o limite de arquivos para indexação

	// Adicionar callback para log de progresso
	scanOptions.OnFileProcessed = func(info utils.FileInfo) {
		pi.logger.Info("Processando arquivo para indexação",
			zap.String("path", info.Path),
			zap.Int64("size", info.Size),
			zap.String("type", info.Type))
	}

	pi.logger.Info("Iniciando escaneamento do projeto", zap.String("path", projectPath))
	startTime := time.Now()

	files, err := utils.ProcessDirectory(projectPath, scanOptions)
	if err != nil {
		return fmt.Errorf("erro ao processar diretório: %w", err)
	}

	pi.logger.Info("Escaneamento concluído",
		zap.Int("fileCount", len(files)),
		zap.Duration("duration", time.Since(startTime)))

	if len(files) == 0 {
		return fmt.Errorf("nenhum arquivo encontrado para indexação")
	}

	// Processar arquivos em chunks e adicionar ao banco de dados vetorial
	pi.logger.Info("Iniciando processamento de chunks...")

	docChan := make(chan vectordb.Document, 100)
	errChan := make(chan error, 1)
	doneChan := make(chan struct{})

	// Iniciar goroutine para coletar documentos
	var allDocs []vectordb.Document
	go func() {
		for doc := range docChan {
			allDocs = append(allDocs, doc)
		}
		close(doneChan)
	}()

	// Iniciar workers para processar arquivos
	var wg sync.WaitGroup

	// Limitar o número de workers para evitar sobrecarregar a API de embeddings
	for i := 0; i < pi.workerCount; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()

			pi.logger.Debug("Iniciando worker", zap.Int("workerID", workerID))

			for j, file := range files {
				// Distribuir arquivos entre workers (round-robin simples)
				if j%pi.workerCount != workerID {
					continue
				}

				// Verificar cancelamento de contexto
				select {
				case <-ctx.Done():
					errChan <- ctx.Err()
					return
				default:
					// Continuar processamento
				}

				metadata := map[string]string{
					"path":      file.Path,
					"type":      file.Type,
					"size":      fmt.Sprintf("%d", file.Size),
					"extension": filepath.Ext(file.Path),
					"filename":  filepath.Base(file.Path),
				}

				// Skip empty files
				if len(strings.TrimSpace(file.Content)) == 0 {
					continue
				}

				// Usar o chunker apropriado com base no tipo de arquivo
				var chunks []chunker.TextChunk
				if isCodeFile(file.Type) {
					chunks = pi.codeChunker.ChunkText(file.Content, metadata)
				} else {
					chunks = pi.chunker.ChunkText(file.Content, metadata)
				}

				// Converter chunks para documentos
				for i, chunk := range chunks {
					doc := vectordb.Document{
						ID:       fmt.Sprintf("%s-%d", filepath.Base(file.Path), i),
						Path:     file.Path,
						Content:  chunk.Content,
						Metadata: chunk.Metadata,
					}
					// Adicionar informação de chunk
					doc.Metadata["chunk_index"] = fmt.Sprintf("%d", i)
					doc.Metadata["total_chunks"] = fmt.Sprintf("%d", len(chunks))

					// Enviar para o canal de documentos
					docChan <- doc
				}

				pi.logger.Debug("Arquivo processado",
					zap.String("path", file.Path),
					zap.Int("chunks", len(chunks)))
			}

			pi.logger.Debug("Worker concluído", zap.Int("workerID", workerID))
		}(i)
	}

	// Aguardar todos os workers terminarem
	go func() {
		wg.Wait()
		close(docChan)
	}()

	// Aguardar conclusão ou erro
	select {
	case err := <-errChan:
		return fmt.Errorf("erro durante o processamento de arquivos: %w", err)
	case <-doneChan:
		// Processamento concluído com sucesso
	}

	// Log antes de adicionar ao banco de dados
	pi.logger.Info("Chunks processados, adicionando ao banco de dados vetorial",
		zap.Int("totalChunks", len(allDocs)))

	// Adicionar todos os documentos ao banco de dados vetorial em batches para evitar OOM
	const batchSize = 50
	for i := 0; i < len(allDocs); i += batchSize {
		end := i + batchSize
		if end > len(allDocs) {
			end = len(allDocs)
		}

		batch := allDocs[i:end]

		pi.logger.Debug("Adicionando batch de documentos",
			zap.Int("batchSize", len(batch)),
			zap.Int("startIndex", i),
			zap.Int("endIndex", end-1))

		if err := pi.db.AddDocuments(ctx, batch); err != nil {
			return fmt.Errorf("erro ao adicionar documentos ao banco de dados (batch %d-%d): %w", i, end-1, err)
		}
	}

	pi.logger.Info("Indexação concluída com sucesso",
		zap.Int("totalFiles", len(files)),
		zap.Int("totalChunks", len(allDocs)),
		zap.Duration("duration", time.Since(startTime)))

	return nil
}

// isCodeFile determina se um tipo de arquivo é código
func isCodeFile(fileType string) bool {
	codeTypes := map[string]bool{
		"Go": true, "Python": true, "JavaScript": true, "TypeScript": true,
		"React JSX": true, "React TSX": true, "Java": true, "C": true,
		"C++": true, "C Header": true, "C++ Header": true, "C#": true,
		"Ruby": true, "PHP": true, "Rust": true, "Dart": true,
		"Swift": true, "Kotlin": true, "Groovy": true, "Scala": true,
		"Perl": true, "Perl Module": true, "Shell": true, "Bash": true,
		"ZSH": true, "Terraform": true,
	}
	return codeTypes[fileType]
}
