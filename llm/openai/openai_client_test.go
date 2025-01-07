package openai

import (
	"context"
	"github.com/diillson/chatcli/llm/client"
	"testing"

	"github.com/diillson/chatcli/models"
)

func TestOpenAIClient_SendPrompt(t *testing.T) {
	client := &client.MockLLMClient{
		Response: "Resposta Mock OpenAI",
		Err:      nil,
	}

	ctx := context.Background()
	prompt := "Teste de prompt"
	history := []models.Message{}

	response, err := client.SendPrompt(ctx, prompt, history)
	if err != nil {
		t.Errorf("Erro inesperado: %v", err)
	}
	if response != "Resposta Mock OpenAI" {
		t.Errorf("Resposta inesperada: %s", response)
	}
}
