package manager

import (
	"context"
	"github.com/diillson/chatcli/rag/embeddings"
	"github.com/diillson/chatcli/rag/indexer"
	"github.com/diillson/chatcli/rag/retriever"
	"github.com/diillson/chatcli/rag/vectordb"
	"go.uber.org/zap"
	"os"
)

// RagManager coordena os componentes RAG para indexação e recuperação
type RagManager struct {
	logger         *zap.Logger
	vectorDB       vectordb.VectorDB
	embeddingGen   embeddings.EmbeddingGenerator
	projectIndexer *indexer.ProjectIndexer
	retriever      *retriever.HybridRetriever // <-- Mudou para HybridRetriever
	provider       string
}

// NewRagManager cria uma nova instância do RagManager
func NewRagManager(logger *zap.Logger, apiKey string, provider string) *RagManager {
	// Definir qual embedding usar:
	// 1. Para OpenAI, usar a API OpenAI
	// 2. Para Claude, tentar usar OpenAI API se disponível
	// 3. Fallback para embeddings simples
	var embeddingGen embeddings.EmbeddingGenerator
	var useRobustEmbeddings bool = true

	// Tentar usar OpenAI para embeddings (melhor opção)
	openaiKey := apiKey
	if provider != "OPENAI" {
		// Se não estiver usando OpenAI, verificar se temos uma chave OpenAI separada
		openaiKey = os.Getenv("OPENAI_API_KEY")
	}

	if openaiKey != "" {
		logger.Info("Usando embeddings OpenAI para qualidade máxima")
		embeddingGen = embeddings.NewOpenAIEmbedding(openaiKey, logger)
	} else {
		// Fallback para embeddings simples
		logger.Warn("API OpenAI não disponível, usando embeddings simples com qualidade reduzida")
		useRobustEmbeddings = false

		// Para Claude, usar embeddings específicos de Claude
		if provider == "CLAUDEAI" {
			embeddingGen = embeddings.NewClaudeEmbedding(apiKey, logger)
		} else {
			// Para outros provedores, usar embeddings simples
			embeddingGen = embeddings.NewSimpleEmbedding(logger)
		}
	}

	vectorDB := vectordb.NewInMemoryVectorDB(embeddingGen)

	// Criar HybridRetriever em vez do ContextRetriever padrão
	hybridRetriever := retriever.NewHybridRetriever(vectorDB, logger)

	// Se estamos usando embeddings robustos, aumentar o número de documentos retornados
	if useRobustEmbeddings {
		hybridRetriever.MaxDocs = 8 // Aumentar para 8 documentos com embeddings bons
	} else {
		hybridRetriever.MaxDocs = 15 // Aumentar para 15 com embeddings simples para compensar
	}

	return &RagManager{
		logger:         logger,
		vectorDB:       vectorDB,
		embeddingGen:   embeddingGen,
		projectIndexer: indexer.NewProjectIndexer(vectorDB, logger),
		retriever:      hybridRetriever,
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
