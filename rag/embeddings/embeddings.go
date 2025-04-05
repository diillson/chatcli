package embeddings

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/diillson/chatcli/utils"
	"go.uber.org/zap"
)

// EmbeddingGenerator interface para diferentes provedores de embeddings
type EmbeddingGenerator interface {
	GenerateEmbeddings(ctx context.Context, texts []string) ([][]float32, error)
	Dimensions() int
}

// OpenAIEmbedding implementa EmbeddingGenerator para OpenAI
type OpenAIEmbedding struct {
	apiKey  string
	model   string
	logger  *zap.Logger
	client  *http.Client
	dimSize int
}

// NewOpenAIEmbedding cria uma nova instância do gerador de embeddings OpenAI
func NewOpenAIEmbedding(apiKey string, logger *zap.Logger) *OpenAIEmbedding {
	return &OpenAIEmbedding{
		apiKey:  apiKey,
		model:   "text-embedding-3-small", // Modelo padrão
		logger:  logger,
		client:  utils.NewHTTPClient(logger, 60*time.Second),
		dimSize: 1536, // Dimensionalidade do modelo padrão
	}
}

// GenerateEmbeddings gera embeddings usando a API de embeddings da OpenAI
func (o *OpenAIEmbedding) GenerateEmbeddings(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return [][]float32{}, nil
	}

	// Processar em lotes de no máximo 20 textos para evitar exceder limites da API
	const batchSize = 20
	var allEmbeddings [][]float32

	for i := 0; i < len(texts); i += batchSize {
		end := i + batchSize
		if end > len(texts) {
			end = len(texts)
		}

		batch := texts[i:end]
		embeddings, err := o.processBatch(ctx, batch)
		if err != nil {
			return nil, err
		}

		allEmbeddings = append(allEmbeddings, embeddings...)
	}

	return allEmbeddings, nil
}

// processBatch processa um lote de textos para obter embeddings
func (o *OpenAIEmbedding) processBatch(ctx context.Context, texts []string) ([][]float32, error) {
	url := "https://api.openai.com/v1/embeddings"

	requestBody := map[string]interface{}{
		"model": o.model,
		"input": texts,
	}

	jsonData, err := json.Marshal(requestBody)
	if err != nil {
		return nil, fmt.Errorf("erro ao serializar requisição: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("erro ao criar requisição: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+o.apiKey)

	// Implementar retry básico para erros temporários
	maxRetries := 3
	var resp *http.Response

	for retry := 0; retry < maxRetries; retry++ {
		resp, err = o.client.Do(req)
		if err == nil {
			break
		}

		// Verificar se é um erro temporário
		if !utils.IsTemporaryError(err) {
			return nil, fmt.Errorf("erro ao enviar requisição: %w", err)
		}

		// Exponential backoff
		if retry < maxRetries-1 {
			backoff := time.Duration(1<<uint(retry)) * time.Second
			time.Sleep(backoff)
		}
	}

	if err != nil {
		return nil, fmt.Errorf("erro ao enviar requisição após %d tentativas: %w", maxRetries, err)
	}

	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("erro na API (status %d): %s", resp.StatusCode, string(body))
	}

	var result struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("erro ao decodificar resposta: %w", err)
	}

	embeddings := make([][]float32, len(result.Data))
	for i, item := range result.Data {
		embeddings[i] = item.Embedding
	}

	return embeddings, nil
}

// Dimensions retorna o tamanho do vetor de embeddings
func (o *OpenAIEmbedding) Dimensions() int {
	return o.dimSize
}

type ClaudeEmbedding struct {
	apiKey  string
	logger  *zap.Logger
	client  *http.Client
	dimSize int
}

// NewClaudeEmbedding cria uma nova instância para Claude embeddings
func NewClaudeEmbedding(apiKey string, logger *zap.Logger) *ClaudeEmbedding {
	// Nota: para Claude, extraímos embeddings localmente
	// ou através de uma API intermediária
	return &ClaudeEmbedding{
		apiKey:  apiKey,
		logger:  logger,
		client:  utils.NewHTTPClient(logger, 60*time.Second),
		dimSize: 768, // Tamanho de embedding menor para economia
	}
}

// GenerateEmbeddings para Claude (simplificado para funcionar com a chave Claude)
func (c *ClaudeEmbedding) GenerateEmbeddings(ctx context.Context, texts []string) ([][]float32, error) {
	// Para implementação simplificada, podemos usar uma biblioteca local
	embeddings := make([][]float32, len(texts))

	// Método simples: hashing consistente do texto para criar vetores
	for i, text := range texts {
		// Gera um vetor muito simples, não tão eficaz quanto o da OpenAI,
		// mas evita depender da API OpenAI quando estamos usando o Claude
		embeddings[i] = generateSimpleEmbedding(text, c.dimSize)
	}

	return embeddings, nil
}

// Dimensions retorna a dimensão dos embeddings
func (c *ClaudeEmbedding) Dimensions() int {
	return c.dimSize
}

// SimpleEmbedding é uma implementação local de fallback
type SimpleEmbedding struct {
	logger  *zap.Logger
	dimSize int
}

// NewSimpleEmbedding cria um gerador de embeddings simples local
func NewSimpleEmbedding(logger *zap.Logger) *SimpleEmbedding {
	return &SimpleEmbedding{
		logger:  logger,
		dimSize: 512, // Menor dimensionalidade para este método simples
	}
}

// GenerateEmbeddings gera embeddings básicos baseados em hashing
func (s *SimpleEmbedding) GenerateEmbeddings(ctx context.Context, texts []string) ([][]float32, error) {
	embeddings := make([][]float32, len(texts))

	for i, text := range texts {
		embeddings[i] = generateSimpleEmbedding(text, s.dimSize)
	}

	return embeddings, nil
}

// Dimensions retorna a dimensão dos embeddings
func (s *SimpleEmbedding) Dimensions() int {
	return s.dimSize
}

// generateSimpleEmbedding é uma função auxiliar para gerar um embedding
// baseado em hashing do texto (método muito simples, não ideal para produção)
func generateSimpleEmbedding(text string, dimensions int) []float32 {
	// Normalizar o texto
	text = strings.ToLower(text)
	embedding := make([]float32, dimensions)

	// Usar hash para gerar valores pseudo-aleatórios consistentes
	h := fnv.New32a()
	words := strings.Fields(text)

	// Para cada palavra, atualizar o embedding
	for _, word := range words {
		h.Reset()
		h.Write([]byte(word))
		hashValue := h.Sum32()

		// Usar o hash para distribuir valores pelo vetor
		for i := 0; i < dimensions; i++ {
			// Rotação do hash para cada dimensão
			rotated := (hashValue + uint32(i)*1299721) % 4294967291
			value := float32(rotated%1000)/500.0 - 1.0 // Valores entre -1 e 1
			embedding[i] += value
		}
	}

	// Normalizar o vetor
	return normalizeVector(embedding)
}

// normalizeVector normaliza um vetor para ter comprimento = 1
func normalizeVector(vector []float32) []float32 {
	var sumSquares float32
	for _, v := range vector {
		sumSquares += v * v
	}

	if sumSquares > 0 {
		magnitude := float32(math.Sqrt(float64(sumSquares)))
		for i := range vector {
			vector[i] /= magnitude
		}
	}

	return vector
}
