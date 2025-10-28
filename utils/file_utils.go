/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
 */
package utils

import (
	"bufio"
	"context"
	"errors"
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

// ProcessDirectory processa um diretório recursivamente de forma concorrente e segura.
func ProcessDirectory(dirPath string, options DirectoryScanOptions) ([]FileInfo, error) {
	dirPath, err := ExpandPath(dirPath)
	if err != nil {
		return nil, fmt.Errorf("erro ao expandir o caminho: %w", err)
	}

	fileInfo, err := os.Stat(dirPath)
	if err != nil {
		return nil, fmt.Errorf("erro ao acessar o caminho: %w", err)
	}

	if !fileInfo.IsDir() {
		content, err := ReadFileContent(dirPath, options.MaxTotalSize)
		if err != nil {
			return nil, err
		}
		fileType := DetectFileType(dirPath)
		file := FileInfo{Path: dirPath, Content: content, Size: fileInfo.Size(), Type: fileType}
		if options.OnFileProcessed != nil {
			options.OnFileProcessed(file)
		}
		return []FileInfo{file}, nil
	}

	// Carrega padrões de exclusão customizados e os adiciona às opções.
	customExcludeDirs, customExcludePatterns := loadIgnorePatterns(dirPath, options.Logger)
	options.ExcludeDirs = append(options.ExcludeDirs, customExcludeDirs...)
	options.ExcludePatterns = append(options.ExcludePatterns, customExcludePatterns...)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var (
		result             []FileInfo
		totalSize          int64
		fileCount          int
		filesToProcessChan = make(chan string, 100)
		resultsChan        = make(chan FileInfo, 100)
		wgWorkers          sync.WaitGroup
		workerCount        = 4
	)

	for i := 0; i < workerCount; i++ {
		wgWorkers.Add(1)
		go func() {
			defer wgWorkers.Done()
			for {
				select {
				case path, ok := <-filesToProcessChan:
					if !ok {
						return
					}
					content, err := ReadFileContent(path, options.MaxTotalSize)
					if err != nil {
						options.Logger.Warn("Erro ao ler arquivo, pulando", zap.String("path", path), zap.Error(err))
						continue
					}
					info, err := os.Stat(path)
					if err != nil {
						continue
					}
					resultsChan <- FileInfo{
						Path:    path,
						Content: content,
						Size:    info.Size(),
						Type:    DetectFileType(path),
					}
				case <-ctx.Done():
					return
				}
			}
		}()
	}

	var walkErr error
	var wgWalk sync.WaitGroup
	wgWalk.Add(1)
	go func() {
		defer wgWalk.Done()
		defer close(filesToProcessChan)

		walkErr = filepath.Walk(dirPath, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return nil
			}

			select {
			case <-ctx.Done():
				return errors.New("limite de processamento atingido")
			default:
			}

			if info.IsDir() {
				baseName := info.Name()
				if path != dirPath {
					for _, excludedDir := range options.ExcludeDirs {
						if baseName == excludedDir {
							options.Logger.Debug("Pulando diretório ignorado", zap.String("dir", path))
							return filepath.SkipDir
						}
					}
					if !options.IncludeHidden && strings.HasPrefix(baseName, ".") {
						return filepath.SkipDir
					}
				}
				return nil
			}

			if !matchesAny(path, options.ExcludePatterns) && hasAllowedExtension(path, options.Extensions) {
				filesToProcessChan <- path
			}
			return nil
		})
	}()

	go func() {
		wgWorkers.Wait()
		close(resultsChan)
	}()

	for file := range resultsChan {
		if fileCount >= options.MaxFilesToProcess || totalSize+int64(len(file.Content)) > options.MaxTotalSize {
			cancel()
			break
		}
		result = append(result, file)
		fileCount++
		totalSize += int64(len(file.Content))
		if options.OnFileProcessed != nil {
			options.OnFileProcessed(file)
		}
	}

	wgWalk.Wait()
	if walkErr != nil && walkErr.Error() != "limite de processamento atingido" {
		return nil, walkErr
	}
	return result, nil
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
		if isCodeFile(file.Type) {
			builder.WriteString(fmt.Sprintf("```%s\n%s\n```\n\n",
				getLanguageIdentifier(file.Type), file.Content))
		} else {
			builder.WriteString(fmt.Sprintf("%s\n\n", file.Content))
		}
	}

	return builder.String()
}

// Determina se um tipo de arquivo é código
func isCodeFile(fileType string) bool {
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

// CountMatchingFiles faz uma varredura rápida para contar quantos arquivos em um diretório
// correspondem aos critérios das opções, sem ler seu conteúdo.
func CountMatchingFiles(dirPath string, options DirectoryScanOptions) (int, error) {
	dirPath, err := ExpandPath(dirPath)
	if err != nil {
		return 0, err
	}

	info, err := os.Stat(dirPath)
	if err != nil {
		return 0, err
	}
	if !info.IsDir() {
		return 1, nil
	}

	// Carrega padrões de exclusão customizados e os adiciona às opções.
	customExcludeDirs, customExcludePatterns := loadIgnorePatterns(dirPath, options.Logger)
	options.ExcludeDirs = append(options.ExcludeDirs, customExcludeDirs...)
	options.ExcludePatterns = append(options.ExcludePatterns, customExcludePatterns...)

	count := 0
	err = filepath.Walk(dirPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}

		if info.IsDir() {
			base := filepath.Base(path)
			if path != dirPath {
				for _, dir := range options.ExcludeDirs {
					if base == dir {
						return filepath.SkipDir
					}
				}
				if !options.IncludeHidden && strings.HasPrefix(base, ".") {
					return filepath.SkipDir
				}
			}
			return nil
		}

		if !matchesAny(path, options.ExcludePatterns) && hasAllowedExtension(path, options.Extensions) {
			count++
		}
		return nil
	})

	if err != nil {
		return 0, err
	}
	return count, nil
}

// loadIgnorePatterns localiza e carrega o arquivo .chatcliignore apropriado seguindo uma hierarquia de precedência.
func loadIgnorePatterns(rootDir string, logger *zap.Logger) (excludeDirs []string, excludePatterns []string) {
	// 1. Prioridade Máxima: Variável de Ambiente CHATCLI_IGNORE_PATH
	if ignorePathEnv := os.Getenv("CHATCLI_IGNORE"); ignorePathEnv != "" {
		expandedPath, err := ExpandPath(ignorePathEnv)
		if err != nil {
			logger.Warn("Não foi possível expandir o caminho de CHATCLI_IGNORE.", zap.String("path", ignorePathEnv), zap.Error(err))
			return nil, nil
		}
		logger.Info("Usando arquivo de ignore definido pela variável de ambiente.", zap.String("path", expandedPath))
		return readIgnoreFile(expandedPath, logger)
	}

	// 2. Prioridade Média: Arquivo .chatcliignore no diretório do projeto
	projectIgnorePath := filepath.Join(rootDir, ".chatignore")
	dirs, patterns := readIgnoreFile(projectIgnorePath, logger)
	if dirs != nil || patterns != nil {
		logger.Info("Usando arquivo de ignore específico do projeto.", zap.String("path", projectIgnorePath))
		return dirs, patterns
	}

	// 3. Prioridade Baixa: Arquivo .chatcliignore global no diretório de configuração do usuário
	homeDir, err := GetHomeDir()
	if err == nil {
		globalIgnorePath := filepath.Join(homeDir, ".chatcli", ".chatignore")
		dirs, patterns := readIgnoreFile(globalIgnorePath, logger)
		if dirs != nil || patterns != nil {
			logger.Info("Usando arquivo de ignore global do usuário.", zap.String("path", globalIgnorePath))
			return dirs, patterns
		}
	}

	// 4. Fallback: Nenhum arquivo de ignore encontrado
	return nil, nil
}

// readIgnoreFile lê um único arquivo de ignore e retorna os padrões de exclusão.
// Retorna slices nulos se o arquivo não existir ou não puder ser lido.
func readIgnoreFile(filePath string, logger *zap.Logger) (excludeDirs []string, excludePatterns []string) {
	file, err := os.Open(filePath)
	if err != nil {
		if !os.IsNotExist(err) {
			logger.Warn("Não foi possível abrir o arquivo de ignore, pulando.", zap.String("path", filePath), zap.Error(err))
		}
		return nil, nil
	}
	defer file.Close()

	var dirs, patterns []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasSuffix(line, "/") {
			dirs = append(dirs, strings.TrimSuffix(line, "/"))
		} else {
			patterns = append(patterns, line)
		}
	}

	if err := scanner.Err(); err != nil {
		logger.Warn("Erro ao escanear o arquivo de ignore.", zap.String("path", filePath), zap.Error(err))
	}
	return dirs, patterns
}
