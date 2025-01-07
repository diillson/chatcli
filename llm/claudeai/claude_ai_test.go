package claudeai

import (
	"context"
	"github.com/diillson/chatcli/llm/client"
	"testing"

	"github.com/diillson/chatcli/models"
)

func TestClaudeClient_SendPrompt(t *testing.T) {
	client := &client.MockLLMClient{
		Response: "Resposta Mock ClaudeAI",
		Err:      nil,
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
