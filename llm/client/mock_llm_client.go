package client

import (
	"context"

	"github.com/diillson/chatcli/models"
)

// MockLLMClient é um mock que implementa a interface LLMClient
type MockLLMClient struct {
	Response string
	Err      error
}

func (m *MockLLMClient) GetModelName() string {
	return "MockModel"
}

func (m *MockLLMClient) SendPrompt(ctx context.Context, prompt string, history []models.Message) (string, error) {
	return m.Response, m.Err
}
