package stackspotai

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
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

		// Roteamento para os dois endpoints necessários
		if strings.Contains(r.URL.Path, "/create-execution/test-slug") {
			w.WriteHeader(http.StatusOK)
			_, err := fmt.Fprint(w, `"fake-response-id"`)
			if err != nil {
				return
			}
		} else if strings.Contains(r.URL.Path, "/callback/fake-response-id") {
			w.WriteHeader(http.StatusOK)
			resp := `{
                                    "progress": {"status": "COMPLETED"},
                                    "steps": [{"step_result": {"answer": "Hello from StackSpot!"}}]
                            }`
			_, err := fmt.Fprint(w, resp)
			if err != nil {
				return
			}
		} else {
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	logger, _ := zap.NewDevelopment()
	mockTokenManager := new(token.MockTokenManager)

	// Configurar os retornos do mock
	mockTokenManager.On("GetSlugAndTenantName").Return("test-slug", "test-tenant")
	mockTokenManager.On("GetAccessToken", mock.Anything).Return("fake-token", nil)

	// A chamada a NewStackSpotClient agora está correta, pois espera a interface
	client := NewStackSpotClient(mockTokenManager, "test-slug", logger, 1, 0) // Adicione maxAttempts e backoff
	client.baseURL = server.URL                                               // Injeta a URL base do mock
	client.responseTimeout = 10 * time.Millisecond

	history := []models.Message{{Role: "user", Content: "Hi"}}
	resp, err := client.SendPrompt(context.Background(), "Hi", history, 0)

	assert.NoError(t, err)
	assert.Equal(t, "Hello from StackSpot!", resp)
	mockTokenManager.AssertExpectations(t)
}

func TestStackSpotClient_SendPrompt_RetryOnTemporaryError(t *testing.T) {
	attempt := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt++
		if strings.Contains(r.URL.Path, "/create-execution") {
			// Sempre retorna responseID para prosseguir ao polling
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, `"fake-response-id"`)
			return
		}
		if attempt == 1 {
			// Simular erro temporário no polling (ex: 500)
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(w, `{"error": "Temporary error"}`)
			return
		}
		// Sucesso na segunda tentativa
		w.WriteHeader(http.StatusOK)
		resp := `{"progress": {"status": "COMPLETED"}, "steps": [{"step_result": {"answer": "Success on retry"}}]}`
		fmt.Fprint(w, resp)
	}))
	defer server.Close()

	logger, _ := zap.NewDevelopment()
	mockTokenManager := new(token.MockTokenManager)
	mockTokenManager.On("GetSlugAndTenantName").Return("test-slug", "test-tenant")
	mockTokenManager.On("GetAccessToken", mock.Anything).Return("fake-token", nil)

	client := NewStackSpotClient(mockTokenManager, "test-slug", logger, 2, 10*time.Millisecond) // Adicione maxAttempts e backoff
	client.baseURL = server.URL
	client.responseTimeout = 10 * time.Millisecond

	history := []models.Message{{Role: "user", Content: "Test"}}
	resp, err := client.SendPrompt(context.Background(), "Test", history, 0)

	assert.NoError(t, err)
	assert.Equal(t, "Success on retry", resp)
	assert.Equal(t, 2, attempt, "Should have made two attempts")
	mockTokenManager.AssertExpectations(t)
}
