package models

import "github.com/diillson/chatcli/config"

// Message representa uma mensagem trocada com o modelo de linguagem.
type Message struct {
	Role    string `json:"role"`    // O papel da mensagem, como "user" ou "assistant".
	Content string `json:"content"` // O conteúdo da mensagem.
}

// IsValid valida se a mensagem tem um papel e conteúdo válidos.
func (m *Message) IsValid() bool {
	validRoles := map[string]bool{
		"user":      true,
		"assistant": true,
	}
	return validRoles[m.Role] && m.Content != ""
}

// ResponseData representa os dados de resposta da LLM.
type ResponseData struct {
	Status   string `json:"status"`   // O status da resposta: "processing", "completed", ou "error".
	Response string `json:"response"` // A resposta da LLM, se o status for "completed".
	Message  string `json:"message"`  // Mensagem de erro, se o status for "error".
}

// IsValid valida se o status da resposta é um dos valores esperados.
func (r *ResponseData) IsValid() bool {
	validStatuses := map[string]bool{
		config.StatusProcessing: true,
		config.StatusCompleted:  true,
		config.StatusError:      true,
	}
	return validStatuses[r.Status]
}

// UsageInfo representa informações de uso de tokens retornadas pelas APIs
type UsageInfo struct {
	PromptTokens     int // Tokens usados no prompt
	CompletionTokens int // Tokens usados na resposta
	TotalTokens      int // Total de tokens usados
}
