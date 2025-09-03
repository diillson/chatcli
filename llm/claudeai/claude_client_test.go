package claudeai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/diillson/chatcli/models"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
)

func TestClaudeClient_SendPrompt_Success(t *testing.T) {
	// 1. Criar um servidor de teste
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verificar o método e cabeçalhos
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		assert.Equal(t, "test-api-key", r.Header.Get("x-api-key"))
		assert.NotEmpty(t, r.Header.Get("anthropic-version"))

		// Verificar o corpo da requisição
		var reqBody map[string]interface{}
		err := json.NewDecoder(r.Body).Decode(&reqBody)
		assert.NoError(t, err)
		assert.Equal(t, "claude-3-5-sonnet-20241022", reqBody["model"])
		messages := reqBody["messages"].([]interface{})
		assert.Len(t, messages, 1)
		firstMessage := messages[0].(map[string]interface{})
		assert.Equal(t, "user", firstMessage["role"])
		assert.Equal(t, "Hello", firstMessage["content"])

		// Enviar uma resposta de sucesso
		w.WriteHeader(http.StatusOK)
		w.Header().Set("Content-Type", "application/json")
		response := `{"content": [{"type": "text", "text": "Hi there!"}]}`
		_, _ = w.Write([]byte(response))
	}))
	defer server.Close()

	// 2. Configurar o cliente para usar o servidor de teste
	logger, _ := zap.NewDevelopment()
	client := NewClaudeClient("test-api-key", "claude-3-5-sonnet-20241022", logger, 1, 0)

	// Injetamos a URL do servidor de teste diretamente no cliente.
	client.apiURL = server.URL

	// 3. Executar o teste
	history := []models.Message{{Role: "user", Content: "Hello"}}
	resp, err := client.SendPrompt(context.Background(), "Hello", history)

	// 4. Verificar os resultados
	assert.NoError(t, err)
	assert.Equal(t, "Hi there!", resp)
}

func TestClaudeClient_SendPrompt_RetryOnRateLimit(t *testing.T) {
	attempt := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt++
		if attempt == 1 {
			// Simular erro de rate limit na primeira tentativa
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error": "rate limit"}`))
			return
		}
		// Sucesso na segunda tentativa
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"content": [{"type": "text", "text": "Success on second try"}]}`))
	}))
	defer server.Close()

	logger, _ := zap.NewDevelopment()
	// Configurar para 2 tentativas com backoff mínimo para o teste ser rápido
	client := NewClaudeClient("test-api-key", "claude-3-5-sonnet-20241022", logger, 2, 10*time.Millisecond)

	client.apiURL = server.URL

	resp, err := client.SendPrompt(context.Background(), "Test", []models.Message{{Role: "user", Content: "Test"}})

	assert.NoError(t, err)
	assert.Equal(t, "Success on second try", resp)
	assert.Equal(t, 2, attempt, "Should have made two attempts")
}

func TestClaudeClient_buildMessagesAndSystem(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	client := NewClaudeClient("", "", logger, 1, 0)

	history := []models.Message{
		{Role: "system", Content: "You are a helpful assistant."},
		{Role: "user", Content: "First question"},
		{Role: "assistant", Content: "First answer"},
		{Role: "system", Content: "Be concise."},
		{Role: "user", Content: "Second question"},
	}

	messages, systemStr := client.buildMessagesAndSystem("Second question", history)

	expectedMessages := []map[string]string{
		{"role": "user", "content": "First question"},
		{"role": "assistant", "content": "First answer"},
		{"role": "user", "content": "Second question"},
	}
	expectedSystem := "You are a helpful assistant.\n\nBe concise."

	assert.Equal(t, expectedSystem, systemStr)
	assert.Equal(t, expectedMessages, messages)
}
