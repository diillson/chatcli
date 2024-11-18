package llm

import (
	"context"
	"testing"

	"github.com/diillson/chatcli/models"
)

func TestClaudeClient_SendPrompt(t *testing.T) {
	client := &MockLLMClient{
		response: "Resposta Mock ClaudeAI",
		err:      nil,
	}

	ctx := context.Background()
	prompt := "Teste de prompt"
	history := []models.Message{}

	response, err := client.SendPrompt(ctx, prompt, history)
	if err != nil {
		t.Errorf("Erro inesperado: %v", err)
	}
	if response != "Resposta Mock ClaudeAI" {
		t.Errorf("Resposta inesperada: %s", response)
	}
}
