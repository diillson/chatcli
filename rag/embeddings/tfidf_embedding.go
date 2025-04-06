package embeddings

import (
	"context"
	"math"
	"regexp"
	"sort"
	"strings"

	"go.uber.org/zap"
)

// TFIDFEmbedding implementa um gerador de embeddings baseado em TF-IDF
type TFIDFEmbedding struct {
	logger      *zap.Logger
	dimSize     int
	vocabulary  map[string]int // mapeia termos para índices
	idfValues   []float32      // valores IDF para cada termo
	documents   []string       // armazena documentos para calcular IDF
	initialized bool
}

// NewTFIDFEmbedding cria um novo gerador de embeddings baseado em TF-IDF
func NewTFIDFEmbedding(logger *zap.Logger) *TFIDFEmbedding {
	return &TFIDFEmbedding{
		logger:      logger,
		dimSize:     512, // Tamanho fixo para vetores
		vocabulary:  make(map[string]int),
		documents:   []string{},
		initialized: false,
	}
}

// Dimensions retorna a dimensão dos embeddings
func (t *TFIDFEmbedding) Dimensions() int {
	return t.dimSize
}

// GenerateEmbeddings gera embeddings TF-IDF para os textos fornecidos
func (t *TFIDFEmbedding) GenerateEmbeddings(ctx context.Context, texts []string) ([][]float32, error) {
	// Adicionar novos documentos
	t.addDocuments(texts)

	// Certificar que o vocabulário e IDF estão inicializados
	if !t.initialized {
		t.buildVocabularyAndIDF()
	}

	embeddings := make([][]float32, len(texts))

	for i, text := range texts {
		// Calcular TF para este documento
		termFreq := t.calculateTermFrequency(text)

		// Criar embedding usando TF-IDF
		embedding := make([]float32, t.dimSize)
		for term, tf := range termFreq {
			if idx, ok := t.vocabulary[term]; ok && idx < t.dimSize {
				embedding[idx] = tf * t.idfValues[idx]
			}
		}

		// Normalizar vetor
		embeddings[i] = normalizeVector(embedding)
	}

	return embeddings, nil
}

// addDocuments adiciona documentos ao corpus para cálculo de IDF
func (t *TFIDFEmbedding) addDocuments(texts []string) {
	t.documents = append(t.documents, texts...)
	t.initialized = false // Forçar recálculo
}

// buildVocabularyAndIDF constrói o vocabulário e calcula valores IDF
func (t *TFIDFEmbedding) buildVocabularyAndIDF() {
	// Contar ocorrências de documentos para cada termo
	termDocCount := make(map[string]int)

	// Construir vocabulário
	for _, doc := range t.documents {
		terms := t.tokenize(doc)

		// Contar documento apenas uma vez por termo
		seenTerms := make(map[string]bool)
		for _, term := range terms {
			if !seenTerms[term] {
				termDocCount[term]++
				seenTerms[term] = true
			}

			// Adicionar ao vocabulário se novo
			if _, exists := t.vocabulary[term]; !exists {
				t.vocabulary[term] = len(t.vocabulary)
			}
		}
	}

	// Limitar vocabulário ao tamanho máximo do vetor
	if len(t.vocabulary) > t.dimSize {
		// Ordenar termos por frequência e manter apenas os mais comuns
		type TermCount struct {
			Term  string
			Count int
		}

		termCounts := make([]TermCount, 0, len(termDocCount))
		for term, count := range termDocCount {
			termCounts = append(termCounts, TermCount{term, count})
		}

		sort.Slice(termCounts, func(i, j int) bool {
			return termCounts[i].Count > termCounts[j].Count
		})

		// Reconstruir vocabulário com termos mais frequentes
		t.vocabulary = make(map[string]int)
		for i := 0; i < t.dimSize && i < len(termCounts); i++ {
			t.vocabulary[termCounts[i].Term] = i
		}
	}

	// Calcular valores IDF
	t.idfValues = make([]float32, t.dimSize)
	numDocs := float32(len(t.documents))
	if numDocs == 0 {
		numDocs = 1 // Evitar divisão por zero
	}

	for term, idx := range t.vocabulary {
		if idx < t.dimSize {
			docCount := float32(termDocCount[term])
			if docCount > 0 {
				// Fórmula IDF: log(N/df)
				t.idfValues[idx] = float32(math.Log(float64(numDocs / docCount)))
			}
		}
	}

	t.initialized = true
}

// calculateTermFrequency calcula a frequência dos termos em um texto
func (t *TFIDFEmbedding) calculateTermFrequency(text string) map[string]float32 {
	terms := t.tokenize(text)
	termFreq := make(map[string]float32)

	// Contar frequência de cada termo
	for _, term := range terms {
		termFreq[term]++
	}

	// Normalizar pela contagem total
	totalTerms := float32(len(terms))
	if totalTerms > 0 {
		for term := range termFreq {
			termFreq[term] /= totalTerms
		}
	}

	return termFreq
}

// tokenize divide o texto em tokens/termos
func (t *TFIDFEmbedding) tokenize(text string) []string {
	// Normalizar e tokenizar texto
	text = strings.ToLower(text)

	// Remover pontuação e dividir em tokens
	text = regexp.MustCompile(`[^\w\s]`).ReplaceAllString(text, " ")
	return strings.Fields(text)
}
