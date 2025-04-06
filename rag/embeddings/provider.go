package embeddings

import (
	"go.uber.org/zap"
	"os"
)

// NewEmbeddingProvider retorna o gerador de embeddings mais adequado
// com base no provedor atual e nas chaves de API disponíveis
func NewEmbeddingProvider(logger *zap.Logger, apiKey string, provider string) EmbeddingGenerator {
	logger.Info("Inicializando provedor de embeddings",
		zap.String("provider", provider))

	// Verificar se temos acesso à API OpenAI para embeddings
	openaiKey := apiKey
	if provider != "OPENAI" {
		// Se não estamos usando OpenAI como LLM, verificar se temos uma chave OpenAI separada
		openaiKey = os.Getenv("OPENAI_API_KEY")
	}

	// Usar OpenAI para embeddings se a chave estiver disponível
	if openaiKey != "" {
		logger.Info("Usando embeddings OpenAI para melhor qualidade")
		return NewOpenAIEmbedding(openaiKey, logger)
	}

	// Se estamos usando Claude, usar embeddings específicos para Claude
	if provider == "CLAUDEAI" {
		logger.Warn("Usando embeddings básicos para Claude (qualidade reduzida)")
		return NewClaudeEmbedding(apiKey, logger)
	}

	// Fallback final: Usar embeddings TF-IDF
	logger.Warn("Fallback para embeddings TF-IDF (qualidade limitada)")
	return NewTFIDFEmbedding(logger)
}
