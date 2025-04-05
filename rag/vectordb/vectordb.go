package vectordb

import (
	"context"
	"fmt"
	"math"
	"sort"
	"sync"

	"github.com/diillson/chatcli/rag/embeddings"
)

// Document representa um documento indexado com seu embedding
type Document struct {
	ID        string            `json:"id"`
	Path      string            `json:"path"`
	Content   string            `json:"content"`
	Metadata  map[string]string `json:"metadata"`
	Embedding []float32         `json:"embedding,omitempty"`
}

// VectorDB interface para diferentes implementações de bancos de dados vetoriais
type VectorDB interface {
	AddDocuments(ctx context.Context, docs []Document) error
	Search(ctx context.Context, query string, limit int) ([]Document, error)
	Clear() error
}

// InMemoryVectorDB implementação simples em memória para testes e projetos pequenos
type InMemoryVectorDB struct {
	embedder embeddings.EmbeddingGenerator
	docs     []Document
	mu       sync.RWMutex
}

// NewInMemoryVectorDB cria uma nova instância do banco de dados em memória
func NewInMemoryVectorDB(embedder embeddings.EmbeddingGenerator) *InMemoryVectorDB {
	return &InMemoryVectorDB{
		embedder: embedder,
		docs:     make([]Document, 0),
	}
}

// AddDocuments adiciona documentos ao banco de dados com processamento em lotes
func (db *InMemoryVectorDB) AddDocuments(ctx context.Context, docs []Document) error {
	if len(docs) == 0 {
		return nil
	}

	// Processamento em lotes menores para evitar exceder o contexto
	const batchSize = 10 // Reduzindo o tamanho do lote dramaticamente
	var processingError error

	for i := 0; i < len(docs); i += batchSize {
		end := i + batchSize
		if end > len(docs) {
			end = len(docs)
		}

		batch := docs[i:end]

		// Gerar embeddings para documentos sem embedding neste lote
		var textsToEmbed []string
		var indices []int

		for j, doc := range batch {
			if len(doc.Embedding) == 0 {
				// Truncar conteúdo muito grande para evitar exceder limites
				content := truncateText(doc.Content, 1000) // Limitar a 1000 caracteres
				textsToEmbed = append(textsToEmbed, content)
				indices = append(indices, j)
			}
		}

		if len(textsToEmbed) > 0 {
			embeddings, err := db.embedder.GenerateEmbeddings(ctx, textsToEmbed)
			if err != nil {
				return fmt.Errorf("erro ao gerar embeddings para lote %d-%d: %w", i, end-1, err)
			}

			for j, idx := range indices {
				batch[idx].Embedding = embeddings[j]
			}
		}

		// Adicionar este lote ao banco de dados
		db.mu.Lock()
		db.docs = append(db.docs, batch...)
		db.mu.Unlock()
	}

	return processingError
}

// Função auxiliar para truncar texto
func truncateText(text string, maxChars int) string {
	if len(text) <= maxChars {
		return text
	}
	return text[:maxChars]
}

// Search realiza uma busca por similaridade no banco de dados
func (db *InMemoryVectorDB) Search(ctx context.Context, query string, limit int) ([]Document, error) {
	if len(db.docs) == 0 {
		return []Document{}, nil
	}

	// Gerar embedding para a consulta
	queryEmbeddings, err := db.embedder.GenerateEmbeddings(ctx, []string{query})
	if err != nil {
		return nil, err
	}

	if len(queryEmbeddings) == 0 {
		return nil, fmt.Errorf("nenhum embedding gerado para a consulta")
	}

	queryEmbedding := queryEmbeddings[0]

	// Calcular similaridade com cada documento
	db.mu.RLock()
	defer db.mu.RUnlock()

	type ScoredDoc struct {
		Doc   Document
		Score float32
	}

	var scoredDocs []ScoredDoc

	for _, doc := range db.docs {
		similarity := computeSimilarity(queryEmbedding, doc.Embedding)
		scoredDocs = append(scoredDocs, ScoredDoc{
			Doc:   doc,
			Score: similarity,
		})
	}

	// Ordenar por pontuação (maior primeiro)
	sort.Slice(scoredDocs, func(i, j int) bool {
		return scoredDocs[i].Score > scoredDocs[j].Score
	})

	// Limitar resultados
	if limit > len(scoredDocs) {
		limit = len(scoredDocs)
	}

	// Extrair documentos mais relevantes
	results := make([]Document, limit)
	for i := 0; i < limit; i++ {
		results[i] = scoredDocs[i].Doc
	}

	return results, nil
}

// Clear limpa o banco de dados
func (db *InMemoryVectorDB) Clear() error {
	db.mu.Lock()
	defer db.mu.Unlock()
	db.docs = make([]Document, 0)
	return nil
}

// computeSimilarity calcula a similaridade de cosseno entre dois vetores
func computeSimilarity(a, b []float32) float32 {
	if len(a) != len(b) {
		return 0
	}

	var dotProduct float32
	var normA float32
	var normB float32

	for i := 0; i < len(a); i++ {
		dotProduct += a[i] * b[i]
		normA += a[i] * a[i]
		normB += b[i] * b[i]
	}

	if normA == 0 || normB == 0 {
		return 0
	}

	return dotProduct / (float32(math.Sqrt(float64(normA))) * float32(math.Sqrt(float64(normB))))
}
