// llm/llm_client.go
package llm

import (
	"context"
	"github.com/diillson/chatcli/models"
)

// LLMClient define os métodos que um cliente LLM deve implementar
type LLMClient interface {
	GetModelName() string
	SendPrompt(ctx context.Context, prompt string, history []models.Message) (string, error)
}
