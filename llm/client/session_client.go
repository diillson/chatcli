package client

import (
	"context"
	"fmt"
	"github.com/diillson/chatcli/llm/session"
	"github.com/diillson/chatcli/models"
)

// SessionClient é um wrapper do cliente LLM que usa gerenciamento de sessão
type SessionClient struct {
	sessionManager session.SessionManager
	modelName      string
}

// NewSessionClient cria um novo cliente de sessão
func NewSessionClient(sessionManager session.SessionManager) *SessionClient {
	return &SessionClient{
		sessionManager: sessionManager,
		modelName:      sessionManager.GetModelName(),
	}
}

// GetModelName retorna o nome do modelo
func (c *SessionClient) GetModelName() string {
	return c.modelName
}

// SendPrompt envia um prompt para o modelo e mantém o contexto no lado do servidor
func (c *SessionClient) SendPrompt(ctx context.Context, prompt string, history []models.Message) (string, error) {
	// Verifica se já existe uma sessão ativa
	if !c.sessionManager.IsSessionActive() {
		// Inicializa uma nova sessão
		if err := c.sessionManager.InitializeSession(ctx); err != nil {
			return "", fmt.Errorf("falha ao inicializar sessão: %w", err)
		}
	}

	// Envia a mensagem para o gerenciador de sessão
	// Não precisamos enviar o histórico, pois o servidor mantém o contexto
	response, err := c.sessionManager.SendMessage(ctx, prompt)
	if err != nil {
		// Se houver erro, tenta reinicializar a sessão e enviar novamente
		if initErr := c.sessionManager.InitializeSession(ctx); initErr != nil {
			return "", fmt.Errorf("falha ao reinicializar sessão após erro: %w (erro original: %v)", initErr, err)
		}

		// Tenta enviar novamente com a nova sessão
		response, err = c.sessionManager.SendMessage(ctx, prompt)
		if err != nil {
			return "", fmt.Errorf("falha ao enviar prompt após reinicialização da sessão: %w", err)
		}
	}

	return response, nil
}
