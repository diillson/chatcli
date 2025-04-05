package session

import (
	"context"
	"fmt"
)

// SessionError representa um erro relacionado a sessões
type SessionError struct {
	Code    int
	Message string
}

// Error implementa a interface de erro para SessionError
func (e *SessionError) Error() string {
	return fmt.Sprintf("SessionError: %d - %s", e.Code, e.Message)
}

// SessionManager define a interface para gerenciar sessões de conversação com LLMs
type SessionManager interface {
	// InitializeSession inicializa uma nova sessão no servidor LLM
	InitializeSession(ctx context.Context) error

	// SendMessage envia uma mensagem dentro da sessão atual
	// Não é necessário fornecer contexto completo, pois o servidor mantém o histórico
	SendMessage(ctx context.Context, message string) (string, error)

	// GetSessionID retorna o ID da sessão atual, se houver
	GetSessionID() string

	// IsSessionActive verifica se a sessão atual está ativa
	IsSessionActive() bool

	// EndSession encerra a sessão atual
	EndSession(ctx context.Context) error

	// GetModelName retorna o nome do modelo usado na sessão
	GetModelName() string
}
