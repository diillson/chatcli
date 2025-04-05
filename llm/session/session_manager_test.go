package session

import (
	"context"
	"testing"
)

// MockSessionManager é um mock para SessionManager para testes
type MockSessionManager struct {
	InitializeFunc      func(ctx context.Context) error
	SendMessageFunc     func(ctx context.Context, message string) (string, error)
	GetSessionIDFunc    func() string
	IsSessionActiveFunc func() bool
	EndSessionFunc      func(ctx context.Context) error
	GetModelNameFunc    func() string
}

func (m *MockSessionManager) InitializeSession(ctx context.Context) error {
	if m.InitializeFunc != nil {
		return m.InitializeFunc(ctx)
	}
	return nil
}

func (m *MockSessionManager) SendMessage(ctx context.Context, message string) (string, error) {
	if m.SendMessageFunc != nil {
		return m.SendMessageFunc(ctx, message)
	}
	return "Mock response", nil
}

func (m *MockSessionManager) GetSessionID() string {
	if m.GetSessionIDFunc != nil {
		return m.GetSessionIDFunc()
	}
	return "mock-session-id"
}

func (m *MockSessionManager) IsSessionActive() bool {
	if m.IsSessionActiveFunc != nil {
		return m.IsSessionActiveFunc()
	}
	return true
}

func (m *MockSessionManager) EndSession(ctx context.Context) error {
	if m.EndSessionFunc != nil {
		return m.EndSessionFunc(ctx)
	}
	return nil
}

func (m *MockSessionManager) GetModelName() string {
	if m.GetModelNameFunc != nil {
		return m.GetModelNameFunc()
	}
	return "mock-model"
}

func TestSessionManager(t *testing.T) {
	// Este teste apenas verifica se a interface está corretamente definida
	var _ SessionManager = &MockSessionManager{}
}
