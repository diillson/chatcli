package retriever

import (
	"context"
	"fmt"
	"strings"

	"github.com/diillson/chatcli/rag/vectordb"
	"go.uber.org/zap"
)

// ContextRetriever gerencia a recuperação de contexto
type ContextRetriever struct {
	db      vectordb.VectorDB
	logger  *zap.Logger
	maxDocs int
}

// NewContextRetriever cria uma nova instância do ContextRetriever
func NewContextRetriever(db vectordb.VectorDB, logger *zap.Logger) *ContextRetriever {
	return &ContextRetriever{
		db:      db,
		logger:  logger,
		maxDocs: 5, // Recuperar 5 documentos por consulta por padrão
	}
}

// RetrieveContext recupera o contexto relevante para uma consulta
func (cr *ContextRetriever) RetrieveContext(ctx context.Context, query string) (string, error) {
	cr.logger.Info("Executando consulta", zap.String("query", query))

	docs, err := cr.db.Search(ctx, query, cr.maxDocs)
	if err != nil {
		return "", fmt.Errorf("erro ao buscar documentos: %w", err)
	}

	if len(docs) == 0 {
		return "Nenhum conteúdo relevante encontrado para sua consulta.", nil
	}

	// Formatar o contexto
	var contextBuilder strings.Builder
	contextBuilder.WriteString("### Contexto recuperado relevante para a consulta:\n\n")

	for i, doc := range docs {
		path := doc.Path
		if chunkIndex, ok := doc.Metadata["chunk_index"]; ok && chunkIndex != "" {
			totalChunks := doc.Metadata["total_chunks"]
			if totalChunks == "" {
				totalChunks = "?"
			}
			path = fmt.Sprintf("%s (Chunk %s/%s)", path, chunkIndex, totalChunks)
		}

		contextBuilder.WriteString(fmt.Sprintf("## [%d] %s\n\n", i+1, path))

		// Se for código, formatar com markdown
		if fileType, ok := doc.Metadata["type"]; ok && isCodeFile(fileType) {
			langID := getLangIdentifier(fileType)
			contextBuilder.WriteString(fmt.Sprintf("```%s\n%s\n```\n\n", langID, doc.Content))
		} else {
			contextBuilder.WriteString(doc.Content)
			contextBuilder.WriteString("\n\n")
		}
	}

	cr.logger.Info("Contexto recuperado com sucesso", zap.Int("documentsFound", len(docs)))

	return contextBuilder.String(), nil
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

// getLangIdentifier retorna o identificador de linguagem para markdown
func getLangIdentifier(fileType string) string {
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
