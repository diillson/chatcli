package token

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestTokenManager_GetAccessToken_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		err := r.ParseForm()
		require.NoError(t, err, "Parsing form should not produce an error")
		assert.Equal(t, "test_client_id", r.Form.Get("client_id"))
		assert.Equal(t, "test_client_secret", r.Form.Get("client_secret"))

		w.WriteHeader(http.StatusOK)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"access_token": "new_fake_token", "expires_in": 3600}`)
	}))
	defer server.Close()

	logger, _ := zap.NewDevelopment()
	// NewTokenManager retorna a interface Manager
	tmInterface := NewTokenManager("test_client_id", "test_client_secret", "slug", "tenant", logger)

	// Fazemos um type assertion para acessar a struct concreta e seus campos.
	tm, ok := tmInterface.(*TokenManager)
	require.True(t, ok, "NewTokenManager should return a concrete *TokenManager")

	// Agora podemos acessar o campo não exportado para o teste.
	tm.tokenURLOverride = server.URL

	token, err := tm.GetAccessToken(context.Background())
	assert.NoError(t, err)
	assert.Equal(t, "new_fake_token", token)

	token2, err := tm.GetAccessToken(context.Background())
	assert.NoError(t, err)
	assert.Equal(t, "new_fake_token", token2)
}

func TestTokenManager_RefreshToken_Handles401(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error": "invalid_credentials"}`)
	}))
	defer server.Close()

	logger, _ := zap.NewDevelopment()
	tmInterface := NewTokenManager("bad_id", "bad_secret", "slug", "tenant", logger)

	tm, ok := tmInterface.(*TokenManager)
	require.True(t, ok)

	tm.tokenURLOverride = server.URL

	_, err := tm.RefreshToken(context.Background())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "status 401")
}
