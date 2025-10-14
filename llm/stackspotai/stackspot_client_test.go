package stackspotai

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/diillson/chatcli/llm/token"
	"github.com/diillson/chatcli/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"go.uber.org/zap"
)

func TestStackSpotClient_SendPrompt_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer fake-token", r.Header.Get("Authorization"))
		// A URL completa agora será /v1/agent/...
		assert.Equal(t, "/v1/agent/fake-agent-id/chat", r.URL.Path)
		w.WriteHeader(http.StatusOK)
		resp := `{"message": "Hello from StackSpot Agent!"}`
		_, err := fmt.Fprint(w, resp)
		if err != nil {
			return
		}
	}))
	defer server.Close()

	logger, _ := zap.NewDevelopment()
	mockTokenManager := new(token.MockTokenManager)

	mockTokenManager.On("GetAccessToken", mock.Anything).Return("fake-token", nil)

	client := NewStackSpotClient(mockTokenManager, "fake-agent-id", logger, 1, 0)
	// CORRIGIDO: A baseURL deve incluir o /v1 para simular o comportamento real
	client.baseURL = server.URL + "/v1"

	history := []models.Message{{Role: "user", Content: "Hi"}}
	resp, err := client.SendPrompt(context.Background(), "Hi", history, 0)

	assert.NoError(t, err)
	assert.Equal(t, "Hello from StackSpot Agent!", resp)
	mockTokenManager.AssertExpectations(t)
}

func TestStackSpotClient_SendPrompt_RetryOnTemporaryError(t *testing.T) {
	attempt := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt++
		if attempt == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(w, `{"error": "Temporary error"}`)
			return
		}
		w.WriteHeader(http.StatusOK)
		resp := `{"message": "Success on retry"}`
		fmt.Fprint(w, resp)
	}))
	defer server.Close()

	logger, _ := zap.NewDevelopment()
	mockTokenManager := new(token.MockTokenManager)
	mockTokenManager.On("GetAccessToken", mock.Anything).Return("fake-token", nil).Twice()

	client := NewStackSpotClient(mockTokenManager, "fake-agent-id", logger, 2, 10*time.Millisecond)
	// CORRIGIDO: A baseURL deve incluir o /v1
	client.baseURL = server.URL + "/v1"

	history := []models.Message{{Role: "user", Content: "Test"}}
	resp, err := client.SendPrompt(context.Background(), "Test", history, 0)

	assert.NoError(t, err)
	assert.Equal(t, "Success on retry", resp)
	assert.Equal(t, 2, attempt, "Should have made two attempts")
	mockTokenManager.AssertExpectations(t)
}
