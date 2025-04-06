package retriever

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/diillson/chatcli/rag/vectordb"
	"go.uber.org/zap"
)

// HybridRetriever combina busca semântica com busca por palavra-chave
type HybridRetriever struct {
	db      vectordb.VectorDB
	logger  *zap.Logger
	MaxDocs int
	docs    []vectordb.Document // Cache de documentos para busca textual
}

// NewHybridRetriever cria uma nova instância do HybridRetriever
func NewHybridRetriever(db vectordb.VectorDB, logger *zap.Logger) *HybridRetriever {
	return &HybridRetriever{
		db:      db,
		logger:  logger,
		MaxDocs: 5, // Recuperar 5 documentos por consulta por padrão
		docs:    []vectordb.Document{},
	}
}

// RetrieveContext recupera o contexto relevante para uma consulta
// usando uma combinação de busca semântica e textual
func (hr *HybridRetriever) RetrieveContext(ctx context.Context, query string) (string, error) {
	hr.logger.Info("Executando consulta híbrida", zap.String("query", query))

	// 1. Tentar busca semântica primeiro
	semanticDocs, _ := hr.db.Search(ctx, query, hr.MaxDocs)

	// 2. Verificar se obtivemos resultados válidos
	hasValidResults := len(semanticDocs) > 0
	if !hasValidResults {
		hr.logger.Warn("Busca semântica não retornou resultados, alternando para busca textual")
	}

	// 3. Se a busca semântica falhou ou retornou poucos resultados, adicionar busca textual
	var allDocs []vectordb.Document

	if hasValidResults {
		allDocs = semanticDocs
	} else {
		// Carregar todos os documentos se o cache estiver vazio
		if len(hr.docs) == 0 {
			hr.loadAllDocuments(ctx)
		}

		// Busca textual por palavras-chave
		keywordDocs := hr.keywordSearch(query)
		allDocs = keywordDocs

		if len(keywordDocs) == 0 {
			hr.logger.Warn("Busca por palavras-chave também falhou, retornando todos os documentos")
			// Último recurso: retornar alguns documentos aleatórios
			if len(hr.docs) > hr.MaxDocs {
				allDocs = hr.docs[:hr.MaxDocs]
			} else {
				allDocs = hr.docs
			}
		}
	}

	// Se ainda não temos resultados, mostrar mensagem informativa
	if len(allDocs) == 0 {
		return "Nenhum conteúdo relevante encontrado para sua consulta. Tente reformular ou usar termos mais específicos.", nil
	}

	// Formatar o contexto recuperado
	var contextBuilder strings.Builder
	contextBuilder.WriteString("### Contexto recuperado relevante para a consulta:\n\n")

	// Adicionar explicação sobre o método usado
	if !hasValidResults {
		contextBuilder.WriteString("⚠️ _Usando busca por palavras-chave devido à limitação de embeddings._\n\n")
	}

	for i, doc := range allDocs {
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

	hr.logger.Info("Contexto recuperado com sucesso",
		zap.Int("documentsFound", len(allDocs)),
		zap.Bool("usedSemanticSearch", hasValidResults))

	return contextBuilder.String(), nil
}

// loadAllDocuments carrega todos os documentos do banco de dados para busca textual
func (hr *HybridRetriever) loadAllDocuments(ctx context.Context) {
	// Esta é uma solução simplificada - precisaria de uma implementação real
	// para carregar todos os documentos do VectorDB
	tempDocs, err := hr.db.Search(ctx, "", 1000) // Query vazia com limite alto
	if err != nil {
		hr.logger.Error("Erro ao carregar documentos para busca textual", zap.Error(err))
		return
	}
	hr.docs = tempDocs
}

// keywordSearch realiza busca por palavras-chave nos documentos
func (hr *HybridRetriever) keywordSearch(query string) []vectordb.Document {
	// Preprocessar a consulta
	query = strings.ToLower(query)
	terms := strings.Fields(query)

	// Remover stop words (palavras muito comuns)
	var filteredTerms []string
	stopWords := map[string]bool{
		"o": true, "os": true, "as": true, "um": true, "uma": true,
		"e": true, "de": true, "da": true, "do": true, "em": true, "para": true,
		"com": true, "por": true, "the": true, "of": true, "in": true, "to": true,
		"and": true, "a": true, "for": true, "is": true, "on": true, "that": true,
	}

	for _, term := range terms {
		if !stopWords[term] && len(term) > 2 {
			filteredTerms = append(filteredTerms, term)
		}
	}

	// Resultados e pontuações
	type ScoredDoc struct {
		Doc   vectordb.Document
		Score float32
	}

	var scoredResults []ScoredDoc

	// Avaliar cada documento
	for _, doc := range hr.docs {
		content := strings.ToLower(doc.Content)
		fileName := strings.ToLower(doc.Path)

		var score float32

		// Pontuação por correspondência exata no conteúdo
		for _, term := range filteredTerms {
			matches := strings.Count(content, term)
			score += float32(matches) * 0.5

			// Pontuação extra para correspondências no nome do arquivo
			if strings.Contains(fileName, term) {
				score += 5.0
			}
		}

		// Adicionar aos resultados se tiver alguma correspondência
		if score > 0 {
			scoredResults = append(scoredResults, ScoredDoc{Doc: doc, Score: score})
		}
	}

	// Ordenar resultados por pontuação
	sort.Slice(scoredResults, func(i, j int) bool {
		return scoredResults[i].Score > scoredResults[j].Score
	})

	// Limitar número de resultados
	results := make([]vectordb.Document, 0)
	for i := 0; i < len(scoredResults) && i < hr.MaxDocs; i++ {
		results = append(results, scoredResults[i].Doc)
	}

	return results
}
