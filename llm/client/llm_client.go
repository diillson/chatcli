/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package client

import (
	"context"
	"fmt"

	"github.com/diillson/chatcli/models"
)

// LLMError representa um erro personalizado para o cliente LLM
type LLMError struct {
	Code    int
	Message string
}

// Error implementa a interface de erro para LLMError
func (e *LLMError) Error() string {
	return fmt.Sprintf("LLMError: %d - %s", e.Code, e.Message)
}

// ModelSource indicates where a model listing came from.
type ModelSource string

const (
	ModelSourceAPI     ModelSource = "api"     // fetched dynamically from provider API
	ModelSourceCatalog ModelSource = "catalog" // static catalog fallback
)

// ModelInfo represents a model available from a provider's API.
type ModelInfo struct {
	ID          string
	DisplayName string
	Source      ModelSource // "api" or "catalog"
}

// ModelLister is an optional interface that LLM clients can implement
// to support listing available models from the provider's API.
type ModelLister interface {
	ListModels(ctx context.Context) ([]ModelInfo, error)
}

// LLMClient define os métodos que um cliente LLM deve implementar
type LLMClient interface {
	// GetModelName retorna o nome do modelo de linguagem utilizado pelo cliente.
	GetModelName() string

	// SendPrompt envia um prompt para o modelo de linguagem e retorna a resposta.
	// O contexto (ctx) pode ser usado para controlar o tempo de execução e cancelamento.
	// O histórico (history) contém as mensagens anteriores da conversa.
	// Retorna uma string com a resposta do modelo e um erro, caso ocorra.
	SendPrompt(ctx context.Context, prompt string, history []models.Message, maxTokens int) (string, error)
}
