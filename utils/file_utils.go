package utils

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"go.uber.org/zap"
)

// FileInfo armazena informações e conteúdo de um arquivo
type FileInfo struct {
	Path    string
	Content string
	Size    int64
	Type    string
}

// DirectoryScanOptions configura opções para escaneamento de diretórios
type DirectoryScanOptions struct {
	MaxTotalSize      int64 // Tamanho máximo do conteúdo total (em bytes)
	MaxFilesToProcess int   // Número máximo de arquivos a processar
	Logger            *zap.Logger
	Extensions        []string            // Extensões de arquivo a incluir (vazio = todas)
	ExcludeDirs       []string            // Diretórios a excluir
	ExcludePatterns   []string            // Padrões de nome de arquivo a excluir (ex: "*.tmp")
	IncludeHidden     bool                // Incluir arquivos/diretórios ocultos?
	OnFileProcessed   func(info FileInfo) // Callback para cada arquivo processado
}

// DefaultDirectoryScanOptions retorna opções padrão para escaneamento
func DefaultDirectoryScanOptions(logger *zap.Logger) DirectoryScanOptions {
	return DirectoryScanOptions{
		MaxTotalSize:      10 * 1024 * 1024, // 10MB limite total
		MaxFilesToProcess: 100,              // Máximo 100 arquivos
		Logger:            logger,
		Extensions: []string{
			".go", ".py", ".js", ".ts", ".jsx", ".tsx", ".java", ".c", ".cpp", ".h", ".hpp",
			".cs", ".rb", ".php", ".html", ".css", ".scss", ".less", ".json", ".xml", ".yaml",
			".yml", ".md", ".txt", ".sql", ".sh", ".bash", ".zsh", ".env", ".toml", ".ini",
			".config", ".rs", ".dart", ".swift", ".kt", ".groovy", ".scala", ".pl", ".pm", ".tf",
		},
		ExcludeDirs: []string{
			".git", "node_modules", "vendor", "build", "dist", "target",
			"bin", "obj", ".idea", ".vscode", "__pycache__",
		},
		ExcludePatterns: []string{
			"*.exe", "*.dll", "*.so", "*.dylib", "*.zip", "*.tar", "*.gz", "*.rar",
			"*.jar", "*.war", "*.ear", "*.class", "*.o", "*.a", "*.pyc", "*.pyo",
			"*.bin", "*.dat", "*.db", "*.sqlite", "*.sqlite3", "*.log", "*.tmp",
			"*.bak", "*.swp", "*.swo", "*.swn", "*.lock", "package-lock.json",
			"yarn.lock", "Cargo.lock", "*.sum", "*.mod",
		},
		IncludeHidden: false,
	}
}

// DetectFileType detecta o tipo de arquivo com base na extensão
func DetectFileType(filePath string) string {
	fileTypes := map[string]string{
		".go":     "Go",
		".tf":     "Terraform",
		".py":     "Python",
		".js":     "JavaScript",
		".ts":     "TypeScript",
		".jsx":    "React JSX",
		".tsx":    "React TSX",
		".java":   "Java",
		".c":      "C",
		".cpp":    "C++",
		".h":      "C Header",
		".hpp":    "C++ Header",
		".cs":     "C#",
		".rb":     "Ruby",
		".php":    "PHP",
		".html":   "HTML",
		".css":    "CSS",
		".scss":   "SCSS",
		".less":   "LESS",
		".json":   "JSON",
		".xml":    "XML",
		".yaml":   "YAML",
		".yml":    "YAML",
		".md":     "Markdown",
		".txt":    "Text",
		".sql":    "SQL",
		".sh":     "Shell",
		".bash":   "Bash",
		".zsh":    "ZSH",
		".env":    "Environment",
		".toml":   "TOML",
		".ini":    "INI",
		".config": "Config",
		".rs":     "Rust",
		".dart":   "Dart",
		".swift":  "Swift",
		".kt":     "Kotlin",
		".groovy": "Groovy",
		".scala":  "Scala",
		".pl":     "Perl",
		".pm":     "Perl Module",
	}
	ext := strings.ToLower(filepath.Ext(filePath))
	if fileType, ok := fileTypes[ext]; ok {
		return fileType
	}
	return "Text"
}

// matchesAny verifica se uma string corresponde a algum dos padrões fornecidos
func matchesAny(s string, patterns []string) bool {
	for _, pattern := range patterns {
		if matched, _ := filepath.Match(pattern, filepath.Base(s)); matched {
			return true
		}
	}
	return false
}

// hasAllowedExtension verifica se um arquivo tem uma extensão permitida
func hasAllowedExtension(path string, extensions []string) bool {
	if len(extensions) == 0 {
		return true
	}
	ext := strings.ToLower(filepath.Ext(path))
	for _, allowedExt := range extensions {
		if ext == allowedExt {
			return true
		}
	}
	return false
}

// ProcessDirectory processa um diretório recursivamente, coletando conteúdo de arquivos
// que correspondam aos critérios definidos nas opções.
func ProcessDirectory(dirPath string, options DirectoryScanOptions) ([]FileInfo, error) {
	dirPath, err := ExpandPath(dirPath)
	if err != nil {
		return nil, fmt.Errorf("erro ao expandir o caminho: %w", err)
	}

	// Verificar se o caminho existe e é um diretório
	fileInfo, err := os.Stat(dirPath)
	if err != nil {
		return nil, fmt.Errorf("erro ao acessar o caminho: %w", err)
	}

	// Se for um arquivo único, processá-lo diretamente
	if !fileInfo.IsDir() {
		content, err := ReadFileContent(dirPath, options.MaxTotalSize)
		if err != nil {
			return nil, err
		}
		fileType := DetectFileType(dirPath)
		file := FileInfo{
			Path:    dirPath,
			Content: content,
			Size:    fileInfo.Size(),
			Type:    fileType,
		}

		if options.OnFileProcessed != nil {
			options.OnFileProcessed(file)
		}

		return []FileInfo{file}, nil
	}

	// Estruturas para armazenar resultados e controlar limites
	var (
		result      []FileInfo
		totalSize   int64
		fileCount   int
		mu          sync.Mutex
		filesChan   = make(chan string, 100)
		resultChan  = make(chan FileInfo, 10)
		errorChan   = make(chan error, 1)
		doneChan    = make(chan struct{})
		workerCount = 4 // Número de workers para processamento paralelo
	)

	// Inicia o coletor de resultados
	var wgCollector sync.WaitGroup
	wgCollector.Add(1)
	go func() {
		defer wgCollector.Done()
		for file := range resultChan {
			mu.Lock()
			result = append(result, file)
			totalSize += int64(len(file.Content))
			fileCount++
			mu.Unlock()

			// Notificar sobre o arquivo processado
			if options.OnFileProcessed != nil {
				options.OnFileProcessed(file)
			}
		}
	}()

	// Inicia workers para processar arquivos
	var wgWorkers sync.WaitGroup
	for i := 0; i < workerCount; i++ {
		wgWorkers.Add(1)
		go func() {
			defer wgWorkers.Done()
			for filePath := range filesChan {
				// Verifica se já atingimos os limites
				mu.Lock()
				if fileCount >= options.MaxFilesToProcess || totalSize >= options.MaxTotalSize {
					mu.Unlock()
					continue
				}
				mu.Unlock()

				// Tenta ler o arquivo
				content, err := ReadFileContent(filePath, options.MaxTotalSize-totalSize)
				if err != nil {
					if !os.IsNotExist(err) {
						options.Logger.Warn("Erro ao ler arquivo",
							zap.String("path", filePath),
							zap.Error(err))
					}
					continue
				}

				fileInfo, err := os.Stat(filePath)
				if err != nil {
					continue
				}

				fileType := DetectFileType(filePath)
				file := FileInfo{
					Path:    filePath,
					Content: content,
					Size:    fileInfo.Size(),
					Type:    fileType,
				}

				// Envia o resultado para o coletor
				resultChan <- file
			}
		}()
	}

	// Função para fechar todos os canais quando terminado
	go func() {
		wgWorkers.Wait()   // Espera todos os workers terminarem
		close(resultChan)  // Fecha o canal de resultados
		wgCollector.Wait() // Espera o coletor terminar
		close(doneChan)    // Sinaliza conclusão
	}()

	// Função para verificar se um caminho deve ser ignorado
	shouldSkip := func(path string) bool {
		// Verifica diretórios excluídos
		base := filepath.Base(path)
		for _, dir := range options.ExcludeDirs {
			if base == dir {
				return true
			}
		}

		// Verifica arquivos/diretórios ocultos
		if !options.IncludeHidden && strings.HasPrefix(base, ".") {
			return true
		}

		return false
	}

	// Percorre o diretório recursivamente
	go func() {
		defer close(filesChan)

		err := filepath.Walk(dirPath, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return nil // Continua mesmo com erro em um item específico
			}

			// Se for diretório, verifica se deve ser pulado
			if info.IsDir() {
				if path != dirPath && shouldSkip(path) {
					return filepath.SkipDir
				}
				return nil
			}

			// Verifica se o arquivo corresponde aos critérios
			if matchesAny(path, options.ExcludePatterns) {
				return nil
			}

			if !hasAllowedExtension(path, options.Extensions) {
				return nil
			}

			// Verifica limites antes de processar mais um arquivo
			mu.Lock()
			reachedLimit := fileCount >= options.MaxFilesToProcess || totalSize >= options.MaxTotalSize
			mu.Unlock()

			if reachedLimit {
				return filepath.SkipDir // Para de percorrer se já atingiu limites
			}

			// Envia o arquivo para processamento pelos workers
			filesChan <- path
			return nil
		})

		if err != nil {
			errorChan <- err
		}
	}()

	// Aguarda conclusão ou erro
	select {
	case err := <-errorChan:
		return nil, err
	case <-doneChan:
		// Retornar os arquivos processados
		return result, nil
	}
}

// FormatDirectoryContent formata o conteúdo de vários arquivos em uma única string
// organizada com separadores claros e formatação adequada para cada tipo de arquivo.
func FormatDirectoryContent(files []FileInfo, maxTotalSize int64) string {
	if len(files) == 0 {
		return "Nenhum arquivo encontrado ou todos foram filtrados."
	}

	// Calculando o total para verificar se foi truncado
	var totalSize int64
	for _, file := range files {
		totalSize += int64(len(file.Content))
	}

	var builder strings.Builder

	// Cabeçalho com informações gerais
	if totalSize >= maxTotalSize {
		builder.WriteString(fmt.Sprintf("⚠️ CONTEÚDO TRUNCADO: Limite de tamanho atingido (%.2f MB). Mostrando %d arquivos parcialmente.\n\n",
			float64(maxTotalSize)/1024/1024, len(files)))
	} else {
		builder.WriteString(fmt.Sprintf("📁 CONTEÚDO DO DIRETÓRIO: %d arquivos (%.2f KB total)\n\n",
			len(files), float64(totalSize)/1024))
	}

	// Índice dos arquivos para referência rápida
	builder.WriteString("📑 ÍNDICE DE ARQUIVOS:\n")
	for i, file := range files {
		relPath := file.Path
		builder.WriteString(fmt.Sprintf("%d. %s (%s, %.2f KB)\n",
			i+1, relPath, file.Type, float64(len(file.Content))/1024))
	}
	builder.WriteString("\n")

	// Conteúdo de cada arquivo
	for i, file := range files {
		// Separador claro entre arquivos
		builder.WriteString(fmt.Sprintf("📄 ARQUIVO %d/%d: %s (%s)\n",
			i+1, len(files), file.Path, file.Type))

		// Formatar o conteúdo baseado no tipo do arquivo
		if IsCodeFile(file.Type) {
			builder.WriteString(fmt.Sprintf("```%s\n%s\n```\n\n",
				getLanguageIdentifier(file.Type), file.Content))
		} else {
			builder.WriteString(fmt.Sprintf("%s\n\n", file.Content))
		}
	}

	return builder.String()
}

// Determina se um tipo de arquivo é código
func IsCodeFile(fileType string) bool {
	codeTypes := map[string]bool{
		"Go": true, "Python": true, "JavaScript": true, "TypeScript": true,
		"React JSX": true, "React TSX": true, "Java": true, "C": true,
		"C++": true, "C Header": true, "C++ Header": true, "C#": true,
		"Ruby": true, "PHP": true, "Rust": true, "Dart": true,
		"Swift": true, "Kotlin": true, "Groovy": true, "Scala": true,
		"Perl": true, "Perl Module": true, "Shell": true, "Bash": true,
		"ZSH": true,
	}
	return codeTypes[fileType]
}

// Retorna o identificador de linguagem para code fences do markdown
func getLanguageIdentifier(fileType string) string {
	identifiers := map[string]string{
		"Go": "go", "Terraform": "hcl", "Python": "python", "JavaScript": "javascript",
		"TypeScript": "typescript", "React JSX": "jsx", "React TSX": "tsx",
		"Java": "java", "C": "c", "C++": "cpp", "C Header": "c",
		"C++ Header": "cpp", "C#": "csharp", "Ruby": "ruby",
		"PHP": "php", "HTML": "html", "CSS": "css", "SCSS": "scss",
		"LESS": "less", "JSON": "json", "XML": "xml", "YAML": "yaml",
		"Markdown": "markdown", "SQL": "sql", "Shell": "sh",
		"Bash": "bash", "ZSH": "zsh", "Rust": "rust", "Dart": "dart",
		"Swift": "swift", "Kotlin": "kotlin", "Groovy": "groovy",
		"Scala": "scala", "Perl": "perl", "Perl Module": "perl",
	}

	if id, ok := identifiers[fileType]; ok {
		return id
	}
	return "text"
}

// ShouldSkipDir verifica se um diretório deve ser ignorado
func ShouldSkipDir(dirName string) bool {
	skipDirs := map[string]bool{
		"node_modules": true, ".git": true, "dist": true, "build": true,
		".idea": true, ".vscode": true, "__pycache__": true, "venv": true,
		"env": true, "bin": true, "obj": true, ".next": true, "target": true,
	}

	return skipDirs[dirName] || strings.HasPrefix(dirName, ".")
}
