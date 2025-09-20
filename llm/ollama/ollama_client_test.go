package ollama

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/diillson/chatcli/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestOllamaClient_SendPrompt_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" || r.Method != http.MethodPost {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, err := io.WriteString(w, `{"message":{"role":"assistant","content":"Hello from Ollama!"},"done":true}`)
		if err != nil {
			return
		}
	}))
	defer srv.Close()

	logger, _ := zap.NewDevelopment()
	c := NewClient(srv.URL, "llama3.1:8b", logger, 1, 0)

	out, err := c.SendPrompt(context.Background(), "Hi", []models.Message{{Role: "user", Content: "Hi"}}, 0)
	require.NoError(t, err)
	assert.Equal(t, "Hello from Ollama!", out)
}
