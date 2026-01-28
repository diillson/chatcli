/*
 * ChatCLI - Metrics Token Counter
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
 */
package metrics

import (
	"fmt"
	"sync"

	"github.com/diillson/chatcli/llm/catalog"
)

// TokenCounter rastreia o uso de tokens na sessão
type TokenCounter struct {
	provider         string
	modelName        string
	modelLimit       int
	totalTokens      int // Total acumulado na sessão
	promptTokens     int // Tokens do último prompt
	completionTokens int // Tokens da última resposta
	turns            int // Número de turnos
	mu               sync.Mutex
}

// NewTokenCounter cria um novo contador de tokens.
// Agora aceita 'provider' para buscar o limite correto no catálogo.
func NewTokenCounter(provider, modelName string, overrideLimit int) *TokenCounter {
	limit := getModelLimit(provider, modelName, overrideLimit)
	return &TokenCounter{
		provider:   provider,
		modelName:  modelName,
		modelLimit: limit,
	}
}

// getModelLimit retorna o limite de tokens para um modelo usando o catálogo
func getModelLimit(provider, modelName string, override int) int {
	// 1. Se o usuário forçou um limite, usamos ele
	if override > 0 {
		return override
	}
	// Tenta resolver o modelo no catálogo central
	if meta, ok := catalog.Resolve(provider, modelName); ok && meta.ContextWindow > 0 {
		return meta.ContextWindow
	}

	// Fallback padrão conservador (ex: modelos desconhecidos ou erro no catálogo)
	return 4096
}

// EstimateTokens estima o número de tokens em um texto
// Usa uma aproximação: ~4 caracteres por token (média para inglês/português)
func EstimateTokens(text string) int {
	if text == "" {
		return 0
	}
	// Aproximação: 1 token ~= 4 caracteres
	// Pode ser ajustado para ~3.5 para mais precisão com código
	return len(text) / 4
}

// AddTurn registra um turno de conversa (estimativa baseada em texto)
func (tc *TokenCounter) AddTurn(prompt string, response string) {
	tc.mu.Lock()
	defer tc.mu.Unlock()

	tc.promptTokens = EstimateTokens(prompt)
	tc.completionTokens = EstimateTokens(response)
	tc.totalTokens += tc.promptTokens + tc.completionTokens
	tc.turns++
}

// AddTokens adiciona tokens diretamente (quando API retorna usage real)
func (tc *TokenCounter) AddTokens(promptTokens, completionTokens int) {
	tc.mu.Lock()
	defer tc.mu.Unlock()

	tc.promptTokens = promptTokens
	tc.completionTokens = completionTokens
	tc.totalTokens += promptTokens + completionTokens
	tc.turns++
}

// GetTotalTokens retorna o total de tokens usados
func (tc *TokenCounter) GetTotalTokens() int {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	return tc.totalTokens
}

// GetModelLimit retorna o limite do modelo
func (tc *TokenCounter) GetModelLimit() int {
	return tc.modelLimit
}

// GetUsagePercent retorna a porcentagem de uso da janela de contexto
func (tc *TokenCounter) GetUsagePercent() float64 {
	tc.mu.Lock()
	defer tc.mu.Unlock()

	if tc.modelLimit == 0 {
		return 0
	}
	return float64(tc.totalTokens) / float64(tc.modelLimit) * 100
}

// GetTurns retorna o número de turnos
func (tc *TokenCounter) GetTurns() int {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	return tc.turns
}

// GetLastTurnTokens retorna os tokens do último turno
func (tc *TokenCounter) GetLastTurnTokens() (prompt, completion int) {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	return tc.promptTokens, tc.completionTokens
}

// Reset reinicia o contador
func (tc *TokenCounter) Reset() {
	tc.mu.Lock()
	defer tc.mu.Unlock()

	tc.totalTokens = 0
	tc.promptTokens = 0
	tc.completionTokens = 0
	tc.turns = 0
}

// IsNearLimit verifica se está próximo do limite (70% ou mais)
func (tc *TokenCounter) IsNearLimit() bool {
	return tc.GetUsagePercent() >= 70
}

// IsCritical verifica se está em nível crítico (90% ou mais)
func (tc *TokenCounter) IsCritical() bool {
	return tc.GetUsagePercent() >= 90
}

// FormatTokens formata número de tokens para exibição (12000 -> 12.0k)
func FormatTokens(tokens int) string {
	if tokens < 1000 {
		return fmt.Sprintf("%d", tokens)
	}
	if tokens < 1000000 {
		return fmt.Sprintf("%.1fk", float64(tokens)/1000)
	}
	return fmt.Sprintf("%.1fM", float64(tokens)/1000000)
}
