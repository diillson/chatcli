package utils

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"go.uber.org/zap"
)

// ChunkManager gerencia a divisão e processamento de grandes projetos em chunks
type ChunkManager struct {
	logger           *zap.Logger
	maxTokensPerFile int
	maxTokensTotal   int
	tokensPerChar    float64
	excludeDirs      []string
	excludeExts      []string
	includeExts      []string
}

// FileWithTokens armazena um arquivo com sua estimativa de tokens
type FileWithTokens struct {
	Path        string
	Size        int64
	TokenCount  int
	IsImportant bool
	Type        string
}

// Chunk representa um grupo de arquivos que juntos não excedem o limite de tokens
type Chunk struct {
	Files       []FileWithTokens
	TotalSize   int64
	TotalTokens int
	ChunkIndex  int
}

// NewChunkManager cria um novo gerenciador de chunks
func NewChunkManager(logger *zap.Logger) *ChunkManager {
	return &ChunkManager{
		logger:           logger,
		maxTokensPerFile: 50000,  // Um único arquivo não deve exceder este limite
		maxTokensTotal:   190000, // Deixando margem para o contexto da conversa
		tokensPerChar:    0.25,   // Estimativa conservadora de tokens por caractere
		excludeDirs:      []string{".git", "node_modules", "vendor", "dist", "build", ".idea", ".vscode", "bin", "obj"},
		excludeExts:      []string{".exe", ".dll", ".so", ".dylib", ".zip", ".tar", ".gz", ".jpg", ".png", ".pdf"},
		includeExts:      []string{".go", ".py", ".js", ".ts", ".java", ".c", ".cpp", ".rb", ".php", ".html", ".tf", ".css", ".json", ".yaml", ".yml", ".md", ".txt", ".sql", ".sh"},
	}
}

// EstimateTokens estima o número de tokens em um texto
func (cm *ChunkManager) EstimateTokens(text string) int {
	return int(float64(len(text)) * cm.tokensPerChar)
}

// IsImportantFile determina se um arquivo é importante com base em heurísticas
func (cm *ChunkManager) IsImportantFile(path string) bool {
	base := filepath.Base(path)

	// Arquivos principais são importantes
	importantPatterns := []string{
		"main.go", "app.go", "server.go", "config.go",
		"README.md", "LICENSE", "Dockerfile",
		"go.mod", "package.json", "requirements.txt",
	}

	for _, pattern := range importantPatterns {
		if strings.EqualFold(base, pattern) {
			return true
		}
	}

	// Arquivos em diretórios importantes
	importantDirs := []string{"cmd", "app", "pkg", "internal", "api", "core", "models", "config"}
	dir := filepath.Dir(path)
	dirParts := strings.Split(dir, string(os.PathSeparator))

	for _, part := range dirParts {
		for _, importantDir := range importantDirs {
			if strings.EqualFold(part, importantDir) {
				return true
			}
		}
	}

	return false
}

// ShouldIncludeFile verifica se um arquivo deve ser incluído com base nas configurações
func (cm *ChunkManager) ShouldIncludeFile(path string) bool {
	// Verificar se está em um diretório excluído
	for _, dir := range cm.excludeDirs {
		if strings.Contains(path, string(os.PathSeparator)+dir+string(os.PathSeparator)) ||
			strings.HasSuffix(path, string(os.PathSeparator)+dir) {
			return false
		}
	}

	// Verificar a extensão
	ext := strings.ToLower(filepath.Ext(path))

	// Verificar se a extensão está na lista de excluídos
	for _, excluded := range cm.excludeExts {
		if ext == excluded {
			return false
		}
	}

	// Se temos uma lista de inclusões específicas, verificar
	if len(cm.includeExts) > 0 {
		for _, included := range cm.includeExts {
			if ext == included {
				return true
			}
		}
		return false
	}

	return true
}

// ScanProject escaneia um projeto e coleta informações sobre arquivos e tokens
func (cm *ChunkManager) ScanProject(rootPath string) ([]FileWithTokens, error) {
	var files []FileWithTokens
	var mutex sync.Mutex
	var wg sync.WaitGroup
	filesChan := make(chan string, 100)

	// Goroutine para coletar arquivos
	wg.Add(1)
	go func() {
		defer wg.Done()
		err := filepath.Walk(rootPath, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return nil // Ignorar erros para continuar o scan
			}

			if info.IsDir() {
				base := filepath.Base(path)
				for _, dir := range cm.excludeDirs {
					if base == dir {
						return filepath.SkipDir
					}
				}
				return nil
			}

			if !info.Mode().IsRegular() || info.Size() > 10*1024*1024 { // Ignorar arquivos > 10MB
				return nil
			}

			if cm.ShouldIncludeFile(path) {
				filesChan <- path
			}

			return nil
		})

		if err != nil {
			cm.logger.Error("Erro ao escanear projeto", zap.Error(err))
		}

		close(filesChan)
	}()

	// Goroutines para processamento paralelo
	const numWorkers = 8
	workerWg := sync.WaitGroup{}

	for i := 0; i < numWorkers; i++ {
		workerWg.Add(1)
		go func() {
			defer workerWg.Done()

			for path := range filesChan {
				fileInfo, err := os.Stat(path)
				if err != nil {
					continue
				}

				// Para arquivos grandes, fazer uma estimativa sem ler todo o conteúdo
				if fileInfo.Size() > 1*1024*1024 { // > 1MB
					fileType := DetectFileType(path)
					isImportant := cm.IsImportantFile(path)
					// Estimativa conservadora para arquivos grandes
					tokenCount := int(float64(fileInfo.Size()) * 0.15)

					// Limitar tokens para arquivos muito grandes
					if tokenCount > cm.maxTokensPerFile {
						tokenCount = cm.maxTokensPerFile
					}

					file := FileWithTokens{
						Path:        path,
						Size:        fileInfo.Size(),
						TokenCount:  tokenCount,
						IsImportant: isImportant,
						Type:        fileType,
					}

					mutex.Lock()
					files = append(files, file)
					mutex.Unlock()
					continue
				}

				// Para arquivos menores, ler e fazer estimativa mais precisa
				content, err := os.ReadFile(path)
				if err != nil {
					continue
				}

				fileType := DetectFileType(path)
				isImportant := cm.IsImportantFile(path)
				tokenCount := cm.EstimateTokens(string(content))

				// Limitar tokens para arquivos que excedem o limite
				if tokenCount > cm.maxTokensPerFile {
					tokenCount = cm.maxTokensPerFile
				}

				file := FileWithTokens{
					Path:        path,
					Size:        fileInfo.Size(),
					TokenCount:  tokenCount,
					IsImportant: isImportant,
					Type:        fileType,
				}

				mutex.Lock()
				files = append(files, file)
				mutex.Unlock()
			}
		}()
	}

	workerWg.Wait()
	wg.Wait()

	// Ordenar arquivos: importantes primeiro, depois por tamanho
	sort.Slice(files, func(i, j int) bool {
		if files[i].IsImportant != files[j].IsImportant {
			return files[i].IsImportant
		}
		return files[i].TokenCount > files[j].TokenCount
	})

	return files, nil
}

// CreateChunks divide os arquivos em chunks que não excedam o limite de tokens
func (cm *ChunkManager) CreateChunks(files []FileWithTokens) []Chunk {
	var chunks []Chunk
	currentChunk := Chunk{
		Files:       []FileWithTokens{},
		TotalSize:   0,
		TotalTokens: 0,
		ChunkIndex:  1,
	}

	// Garantir que arquivos importantes estejam no primeiro chunk
	for i, file := range files {
		if !file.IsImportant {
			break
		}

		// Se adicionar este arquivo vai exceder o limite, começar novo chunk
		if currentChunk.TotalTokens+file.TokenCount > cm.maxTokensTotal {
			if len(currentChunk.Files) > 0 {
				chunks = append(chunks, currentChunk)
				currentChunk = Chunk{
					Files:       []FileWithTokens{},
					TotalSize:   0,
					TotalTokens: 0,
					ChunkIndex:  len(chunks) + 1,
				}
			}
		}

		currentChunk.Files = append(currentChunk.Files, file)
		currentChunk.TotalSize += file.Size
		currentChunk.TotalTokens += file.TokenCount

		// Marcar como processado removendo da lista
		files[i] = FileWithTokens{}
	}

	// Remover arquivos processados
	var remainingFiles []FileWithTokens
	for _, file := range files {
		if file.Path != "" {
			remainingFiles = append(remainingFiles, file)
		}
	}
	files = remainingFiles

	// Processar o resto dos arquivos
	for _, file := range files {
		// Se adicionar este arquivo vai exceder o limite, começar novo chunk
		if currentChunk.TotalTokens+file.TokenCount > cm.maxTokensTotal {
			if len(currentChunk.Files) > 0 {
				chunks = append(chunks, currentChunk)
				currentChunk = Chunk{
					Files:       []FileWithTokens{},
					TotalSize:   0,
					TotalTokens: 0,
					ChunkIndex:  len(chunks) + 1,
				}
			}

			// Se o arquivo sozinho já excede o limite, podemos truncá-lo depois
			if file.TokenCount > cm.maxTokensTotal {
				file.TokenCount = cm.maxTokensTotal
			}
		}

		currentChunk.Files = append(currentChunk.Files, file)
		currentChunk.TotalSize += file.Size
		currentChunk.TotalTokens += file.TokenCount
	}

	// Adicionar o último chunk se não estiver vazio
	if len(currentChunk.Files) > 0 {
		chunks = append(chunks, currentChunk)
	}

	return chunks
}

// ProcessChunk processa um chunk de arquivos e retorna o conteúdo formatado
func (cm *ChunkManager) ProcessChunk(chunk Chunk, progressCallback func(string)) (string, error) {
	var fileInfos []FileInfo

	for _, file := range chunk.Files {
		progressCallback(fmt.Sprintf("Processando %s", file.Path))

		content, err := ReadFileContent(file.Path, 0)
		if err != nil {
			cm.logger.Warn("Erro ao ler arquivo", zap.String("path", file.Path), zap.Error(err))
			continue
		}

		// Se o conteúdo do arquivo for muito grande, truncar
		tokenCount := cm.EstimateTokens(content)
		if tokenCount > cm.maxTokensPerFile {
			// Truncar o conteúdo para não exceder o limite
			ratio := float64(cm.maxTokensPerFile) / float64(tokenCount)
			maxChars := int(float64(len(content)) * ratio)
			halfChars := maxChars / 2

			// Pegar metade do início e metade do final
			prefix := content[:halfChars]
			suffix := content[len(content)-halfChars:]
			content = prefix + "\n\n... [conteúdo truncado] ...\n\n" + suffix
		}

		fileInfo := FileInfo{
			Path:    file.Path,
			Content: content,
			Size:    file.Size,
			Type:    file.Type,
		}

		fileInfos = append(fileInfos, fileInfo)
	}

	// Limitar o tamanho total para não exceder o limite
	var builder strings.Builder

	// Informações sobre o chunk
	builder.WriteString(fmt.Sprintf("🧩 CHUNK %d/%d - %d arquivos (%.2f KB, ~%d tokens)\n\n",
		chunk.ChunkIndex, chunk.ChunkIndex, len(chunk.Files),
		float64(chunk.TotalSize)/1024, chunk.TotalTokens))

	// Se for o primeiro chunk, adicionar uma visão geral do projeto
	if chunk.ChunkIndex == 1 {
		builder.WriteString("🔍 VISÃO GERAL DO PROJETO:\n")
		builder.WriteString("Este é o primeiro conjunto de arquivos do projeto. ")
		builder.WriteString("O projeto foi dividido em partes para não exceder o limite de tokens.\n")
		builder.WriteString("Cada parte será processada sequencialmente.\n\n")
	}

	// Formatar o conteúdo dos arquivos
	formattedContent := FormatDirectoryContent(fileInfos, int64(cm.maxTokensTotal))
	builder.WriteString(formattedContent)

	return builder.String(), nil

	// Agora vamos verificar se o resultado final está dentro do limite seguro
	// Supondo que o histórico, prompts do usuário etc. ocupem até 20000 tokens
	maxSafeTokens := cm.maxTokensTotal - 20000
	resultText := builder.String()
	estimatedTokens := cm.EstimateTokens(resultText)

	if estimatedTokens > maxSafeTokens {
		// Precisamos truncar o resultado
		cm.logger.Warn("Resultado do chunk excede o limite seguro de tokens",
			zap.Int("estimatedTokens", estimatedTokens),
			zap.Int("maxSafeTokens", maxSafeTokens))

		// Recalcular para obter um resultado dentro do limite
		ratio := float64(maxSafeTokens) / float64(estimatedTokens)
		maxContentChars := int(float64(len(resultText))*ratio) - 200 // Margem de segurança

		// Truncar o resultado
		truncatedResult := resultText[:maxContentChars] +
			"\n\n... [Conteúdo truncado para respeitar o limite de tokens] ...\n\n" +
			fmt.Sprintf("Nota: O chunk foi truncado de %d tokens estimados para %d tokens.\n",
				estimatedTokens, maxSafeTokens)

		return truncatedResult, nil
	}

	return resultText, nil
}
