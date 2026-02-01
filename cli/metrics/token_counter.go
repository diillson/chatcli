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
	modelLimit       int  // Limite do modelo
	totalTokens      int  // Total acumulado na sessão
	promptTokens     int  // Tokens do último prompt
	completionTokens int  // Tokens da última resposta
	turns            int  // Número de turnos
	usedRealUsage    bool // Se os tokens vieram da API real
	mu               sync.Mutex
}

// NewTokenCounter cria um novo contador de tokens.
func NewTokenCounter(provider, modelName string, overrideLimit int) *TokenCounter {
	limit := getModelLimit(provider, modelName, overrideLimit)
	return &TokenCounter{
		provider:   provider,
		modelName:  modelName,
		modelLimit: limit,
	}
}

func getModelLimit(provider, modelName string, override int) int {
	if override > 0 {
		return override
	}
	if meta, ok := catalog.Resolve(provider, modelName); ok && meta.ContextWindow > 0 {
		return meta.ContextWindow
	}
	return 4096
}

// AddTurn registra um turno de conversa usando tiktoken (fallback)
func (tc *TokenCounter) AddTurn(prompt string, response string) {
	tc.mu.Lock()
	defer tc.mu.Unlock()

	tc.promptTokens = CountTokens(prompt, tc.modelName)
	tc.completionTokens = CountTokens(response, tc.modelName)
	tc.totalTokens += tc.promptTokens + tc.completionTokens
	tc.turns++
	tc.usedRealUsage = false
}

// AddTokens adiciona tokens diretamente (quando API retorna usage real)
func (tc *TokenCounter) AddTokens(promptTokens, completionTokens int) {
	tc.mu.Lock()
	defer tc.mu.Unlock()

	tc.promptTokens = promptTokens
	tc.completionTokens = completionTokens
	tc.totalTokens += promptTokens + completionTokens
	tc.turns++
	tc.usedRealUsage = true
}

// UpdateLastTurnFromAPI atualiza o último turno com tokens reais da API
// Chame após AddTurn() para sobrescrever a estimativa com valores reais
func (tc *TokenCounter) UpdateLastTurnFromAPI(promptTokens, completionTokens int) {
	tc.mu.Lock()
	defer tc.mu.Unlock()

	// Remove a estimativa anterior
	tc.totalTokens -= tc.promptTokens + tc.completionTokens

	// Adiciona valores reais
	tc.promptTokens = promptTokens
	tc.completionTokens = completionTokens
	tc.totalTokens += promptTokens + completionTokens
	tc.usedRealUsage = true
}

// IsUsingRealUsage retorna se o último turno usou tokens reais da API
func (tc *TokenCounter) IsUsingRealUsage() bool {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	return tc.usedRealUsage
}

func (tc *TokenCounter) GetTotalTokens() int {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	return tc.totalTokens
}

func (tc *TokenCounter) GetModelLimit() int {
	return tc.modelLimit
}

func (tc *TokenCounter) GetUsagePercent() float64 {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	if tc.modelLimit == 0 {
		return 0
	}
	return float64(tc.totalTokens) / float64(tc.modelLimit) * 100
}

func (tc *TokenCounter) GetTurns() int {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	return tc.turns
}

func (tc *TokenCounter) GetLastTurnTokens() (prompt, completion int) {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	return tc.promptTokens, tc.completionTokens
}

func (tc *TokenCounter) Reset() {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	tc.totalTokens = 0
	tc.promptTokens = 0
	tc.completionTokens = 0
	tc.turns = 0
	tc.usedRealUsage = false
}

func (tc *TokenCounter) IsNearLimit() bool {
	return tc.GetUsagePercent() >= 70
}

func (tc *TokenCounter) IsCritical() bool {
	return tc.GetUsagePercent() >= 90
}

func FormatTokens(tokens int) string {
	if tokens < 1000 {
		return fmt.Sprintf("%d", tokens)
	}
	if tokens < 1000000 {
		return fmt.Sprintf("%.1fk", float64(tokens)/1000)
	}
	return fmt.Sprintf("%.1fM", float64(tokens)/1000000)
}
