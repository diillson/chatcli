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
		// A API espera 'client_secret', então verificamos isso no corpo do post
		assert.Equal(t, "test_client_key", r.Form.Get("client_secret"))

		w.WriteHeader(http.StatusOK)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"access_token": "new_fake_token", "expires_in": 3600}`)
	}))
	defer server.Close()

	logger, _ := zap.NewDevelopment()
	tmInterface := NewTokenManager("test_client_id", "test_client_key", "test-realm", logger)

	tm, ok := tmInterface.(*TokenManager)
	require.True(t, ok, "NewTokenManager should return a concrete *TokenManager")

	// O override substitui a URL inteira, então o mock não precisa verificar o path
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
	tmInterface := NewTokenManager("bad_id", "bad_key", "test-realm", logger)

	tm, ok := tmInterface.(*TokenManager)
	require.True(t, ok)

	tm.tokenURLOverride = server.URL

	_, err := tm.RefreshToken(context.Background())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "status 401")
}
