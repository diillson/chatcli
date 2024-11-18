package llm

import (
	"context"

	"github.com/diillson/chatcli/models"
)

// MockLLMClient é um mock que implementa a interface LLMClient
type MockLLMClient struct {
	response string
	err      error
}

func (m *MockLLMClient) GetModelName() string {
	return "MockModel"
}

func (m *MockLLMClient) SendPrompt(ctx context.Context, prompt string, history []models.Message) (string, error) {
	return m.response, m.err
}
