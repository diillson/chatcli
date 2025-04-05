package manager

import (
	"context"
	"github.com/diillson/chatcli/rag/embeddings"
	"github.com/diillson/chatcli/rag/indexer"
	"github.com/diillson/chatcli/rag/retriever"
	"github.com/diillson/chatcli/rag/vectordb"
	"go.uber.org/zap"
)

// RagManager coordena os componentes RAG para indexação e recuperação
type RagManager struct {
	logger         *zap.Logger
	vectorDB       vectordb.VectorDB
	embeddingGen   embeddings.EmbeddingGenerator
	projectIndexer *indexer.ProjectIndexer
	retriever      *retriever.ContextRetriever
	provider       string
}

// NewRagManager cria uma nova instância do RagManager
func NewRagManager(logger *zap.Logger, apiKey string, provider string) *RagManager {
	var embeddingGen embeddings.EmbeddingGenerator

	switch provider {
	case "OPENAI":
		embeddingGen = embeddings.NewOpenAIEmbedding(apiKey, logger)
	case "CLAUDEAI":
		// Para o Claude, ainda usamos a API da OpenAI mas com uma implementação específica
		// que encapsula o pré-processamento e pós-processamento específicos do Claude
		embeddingGen = embeddings.NewClaudeEmbedding(apiKey, logger)
	default:
		// Fallback para uma implementação básica local (que pode não ser tão eficaz)
		embeddingGen = embeddings.NewSimpleEmbedding(logger)
	}

	vectorDB := vectordb.NewInMemoryVectorDB(embeddingGen)

	return &RagManager{
		logger:         logger,
		vectorDB:       vectorDB,
		embeddingGen:   embeddingGen,
		projectIndexer: indexer.NewProjectIndexer(vectorDB, logger),
		retriever:      retriever.NewContextRetriever(vectorDB, logger),
		provider:       provider,
	}
}

// GetProvider retorna o nome do provedor atual
func (rm *RagManager) GetProvider() string {
	return rm.provider
}

// IndexProject indexa um projeto para consultas posteriores
func (rm *RagManager) IndexProject(ctx context.Context, projectPath string) error {
	return rm.projectIndexer.IndexProject(ctx, projectPath)
}

// QueryProject recupera contexto relevante de um projeto indexado com base em uma consulta
func (rm *RagManager) QueryProject(ctx context.Context, query string) (string, error) {
	return rm.retriever.RetrieveContext(ctx, query)
}

// ClearIndex limpa o índice atual
func (rm *RagManager) ClearIndex() error {
	return rm.vectorDB.Clear()
}
