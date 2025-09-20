/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
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

// LLMClient define os métodos que um cliente LLM deve implementar
type LLMClient interface {
	// GetModelName retorna o nome do modelo de linguagem utilizado pelo cliente.
	GetModelName() string

	// SendPrompt envia um prompt para o modelo de linguagem e retorna a resposta.
	// O contexto (ctx) pode ser usado para controlar o tempo de execução e cancelamento.
	// O histórico (history) contém as mensagens anteriores da conversa.
	// Retorna uma string com a resposta do modelo e um erro, caso ocorra.
	SendPrompt(ctx context.Context, prompt string, history []models.Message, maxTokens int) (string, error)

	// (Opcional) Initialize pode ser usado para configurar ou autenticar o cliente LLM.
	// Caso o cliente precise de configuração ou autenticação, esse método pode ser implementado.
	// Initialize(config Config) error
}
