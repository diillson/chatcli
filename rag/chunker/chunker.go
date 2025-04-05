package chunker

import (
	"regexp"
	"strings"
)

// Chunker é uma interface para diferentes estratégias de chunking
type Chunker interface {
	ChunkText(text string, metadata map[string]string) []TextChunk
}

// TextChunk representa um pedaço de texto com metadados
type TextChunk struct {
	Content  string
	Metadata map[string]string
}

// RecursiveChunker divide textos recursivamente com base em delimitadores
type RecursiveChunker struct {
	chunkSize      int
	chunkOverlap   int
	primaryDelim   string
	secondaryDelim string
	tertiaryDelim  string
}

// NewRecursiveChunker cria uma nova instância do RecursiveChunker
func NewRecursiveChunker(chunkSize, overlap int) *RecursiveChunker {
	// Reduzir significativamente o tamanho do chunk padrão
	if chunkSize <= 0 {
		chunkSize = 500 // Bem menor que o original
	}
	if overlap <= 0 {
		overlap = 50
	}

	return &RecursiveChunker{
		chunkSize:      chunkSize,
		chunkOverlap:   overlap,
		primaryDelim:   "\n\n", // Parágrafos
		secondaryDelim: "\n",   // Linhas
		tertiaryDelim:  ". ",   // Sentenças
	}
}

// ChunkText divide o texto em chunks usando a abordagem recursiva
func (c *RecursiveChunker) ChunkText(text string, metadata map[string]string) []TextChunk {
	var chunks []TextChunk

	// Tentar dividir pelo delimitador primário (parágrafos)
	parts := strings.Split(text, c.primaryDelim)

	// Se as partes são pequenas, combine-as
	var currentChunk strings.Builder
	var currentMetadata = make(map[string]string)

	// Copiar todos os metadados originais
	for k, v := range metadata {
		currentMetadata[k] = v
	}

	for _, part := range parts {
		// Se a adição faria o chunk atual ficar maior que o tamanho máximo
		if currentChunk.Len()+len(part) > c.chunkSize && currentChunk.Len() > 0 {
			// Adicionar o chunk atual à lista
			chunks = append(chunks, TextChunk{
				Content:  currentChunk.String(),
				Metadata: copyMap(currentMetadata),
			})

			// Iniciar um novo chunk (manter sobreposição)
			overlap := getLastNChars(currentChunk.String(), c.chunkOverlap)
			currentChunk.Reset()
			currentChunk.WriteString(overlap)
		}

		// Adicionar esta parte ao chunk atual
		if currentChunk.Len() > 0 {
			currentChunk.WriteString(c.primaryDelim)
		}
		currentChunk.WriteString(part)
	}

	// Adicionar o último chunk se não estiver vazio
	if currentChunk.Len() > 0 {
		chunks = append(chunks, TextChunk{
			Content:  currentChunk.String(),
			Metadata: copyMap(currentMetadata),
		})
	}

	return chunks
}

// CodeChunker é um chunker especializado para código-fonte
type CodeChunker struct {
	chunkSize    int
	chunkOverlap int
}

// NewCodeChunker cria uma nova instância do CodeChunker
func NewCodeChunker(chunkSize, overlap int) *CodeChunker {
	// Reduzir significativamente o tamanho do chunk padrão
	if chunkSize <= 0 {
		chunkSize = 500 // Bem menor que o original
	}
	if overlap <= 0 {
		overlap = 50
	}

	return &CodeChunker{
		chunkSize:    chunkSize,
		chunkOverlap: overlap,
	}
}

// ChunkText divide o código em chunks semânticos baseados na estrutura do código
func (c *CodeChunker) ChunkText(text string, metadata map[string]string) []TextChunk {
	// Encontrar limites naturais de divisão no código
	var chunks []TextChunk

	// Implementação básica: dividir por funções, classes, etc.
	// Para uma primeira versão, vamos dividir por blocos delimitados por linhas vazias
	// e pelo tamanho máximo do chunk

	lines := strings.Split(text, "\n")
	var currentChunk strings.Builder
	var currentMetadata = copyMap(metadata)

	// Detectar linguagem do arquivo para ajustar a divisão
	fileType, ok := metadata["type"]
	if !ok {
		fileType = "text"
	}

	// Padrões para identificar estruturas no código
	funcPattern := getFunctionPattern(fileType)
	classPattern := getClassPattern(fileType)

	inFunction := false
	inClass := false

	for i, line := range lines {
		// Detectar início de função ou classe
		if funcPattern != nil && funcPattern.MatchString(line) {
			inFunction = true

			// Se já temos conteúdo e não estamos no meio de uma classe, começar novo chunk
			if currentChunk.Len() > c.chunkSize && !inClass {
				chunks = append(chunks, TextChunk{
					Content:  currentChunk.String(),
					Metadata: copyMap(currentMetadata),
				})

				// Manter alguma sobreposição para contexto
				overlap := getLastNLines(currentChunk.String(), 5)
				currentChunk.Reset()
				currentChunk.WriteString(overlap)
			}
		}

		if classPattern != nil && classPattern.MatchString(line) {
			inClass = true

			// Classes geralmente são maiores, então criar novo chunk
			if currentChunk.Len() > 0 {
				chunks = append(chunks, TextChunk{
					Content:  currentChunk.String(),
					Metadata: copyMap(currentMetadata),
				})

				// Manter alguma sobreposição para contexto
				overlap := getLastNLines(currentChunk.String(), 5)
				currentChunk.Reset()
				currentChunk.WriteString(overlap)
			}
		}

		// Detectar possível fim de função ou classe (linha fechando bloco)
		if inFunction && line == "}" {
			inFunction = false
		}

		if inClass && line == "}" {
			inClass = false
		}

		// Adicionar linha ao chunk atual
		if i > 0 {
			currentChunk.WriteString("\n")
		}
		currentChunk.WriteString(line)

		// Se o chunk atual ficar muito grande, divida-o (exceto se estivermos no meio de uma função)
		if currentChunk.Len() >= c.chunkSize && !inFunction {
			chunks = append(chunks, TextChunk{
				Content:  currentChunk.String(),
				Metadata: copyMap(currentMetadata),
			})

			// Manter alguma sobreposição para contexto
			overlap := getLastNLines(currentChunk.String(), 10)
			currentChunk.Reset()
			currentChunk.WriteString(overlap)
		}
	}

	// Adicionar o último chunk se não estiver vazio
	if currentChunk.Len() > 0 {
		chunks = append(chunks, TextChunk{
			Content:  currentChunk.String(),
			Metadata: copyMap(currentMetadata),
		})
	}

	return chunks
}

// Funções auxiliares

// copyMap cria uma cópia de um mapa de strings
func copyMap(m map[string]string) map[string]string {
	result := make(map[string]string)
	for k, v := range m {
		result[k] = v
	}
	return result
}

// getLastNChars retorna os últimos n caracteres de uma string
func getLastNChars(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// getLastNLines retorna as últimas n linhas de um texto
func getLastNLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= n {
		return s
	}
	return strings.Join(lines[len(lines)-n:], "\n")
}

// getFunctionPattern retorna um regex para identificar declarações de função em uma linguagem
func getFunctionPattern(fileType string) *regexp.Regexp {
	var pattern string

	switch fileType {
	case "Go":
		pattern = `^\s*func\s+\w+\s*\(`
	case "JavaScript", "TypeScript":
		pattern = `^\s*(function\s+\w+|const\s+\w+\s*=\s*function|const\s+\w+\s*=\s*\()`
	case "Python":
		pattern = `^\s*def\s+\w+\s*\(`
	case "Java", "C#":
		pattern = `^\s*(public|private|protected)?\s*\w+\s+\w+\s*\(`
	case "C", "C++":
		pattern = `^\s*\w+\s+\w+\s*\(`
	case "Ruby":
		pattern = `^\s*def\s+\w+`
	default:
		return nil
	}

	regex, err := regexp.Compile(pattern)
	if err != nil {
		return nil
	}
	return regex
}

// getClassPattern retorna um regex para identificar declarações de classe em uma linguagem
func getClassPattern(fileType string) *regexp.Regexp {
	var pattern string

	switch fileType {
	case "Go":
		pattern = `^\s*type\s+\w+\s+struct\s*{`
	case "JavaScript", "TypeScript":
		pattern = `^\s*(class\s+\w+|const\s+\w+\s*=\s*class)`
	case "Python":
		pattern = `^\s*class\s+\w+\s*(\(.*\))?\s*:`
	case "Java", "C#":
		pattern = `^\s*(public|private|protected)?\s*class\s+\w+`
	case "C++":
		pattern = `^\s*(class|struct)\s+\w+`
	case "Ruby":
		pattern = `^\s*class\s+\w+`
	default:
		return nil
	}

	regex, err := regexp.Compile(pattern)
	if err != nil {
		return nil
	}
	return regex
}
