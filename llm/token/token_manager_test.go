package token

import (
	"context"
	"fmt"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTokenManager_GetAccessToken_Success(t *testing.T) {
	// 1. Criar um servidor mock
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Validar a requisição
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "application/x-www-form-urlencoded", r.Header.Get("Content-Type"))
		r.ParseForm()
		assert.Equal(t, "test_client_id", r.Form.Get("client_id"))
		assert.Equal(t, "test_client_secret", r.Form.Get("client_secret"))

		// Enviar resposta de sucesso
		w.WriteHeader(http.StatusOK)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"access_token": "new_fake_token", "expires_in": 3600}`)
	}))
	defer server.Close()

	// 2. Criar o TokenManager apontando para o servidor mock
	logger, _ := zap.NewDevelopment()
	// Precisamos de uma forma de injetar a URL, vamos refatorar o TokenManager
	tm := NewTokenManager("test_client_id", "test_client_secret", "slug", "tenant", logger)

	// Refatoração necessária: TokenManager precisa de um campo para a URL base
	// Vamos assumir que adicionamos um campo `tokenURLOverride`
	tm.tokenURLOverride = server.URL // Esta linha requer uma pequena refatoração

	// 3. Executar e validar
	token, err := tm.GetAccessToken(context.Background())
	assert.NoError(t, err)
	assert.Equal(t, "new_fake_token", token)

	// Chamar de novo deve retornar o token do cache
	token2, err := tm.GetAccessToken(context.Background())
	assert.NoError(t, err)
	assert.Equal(t, "new_fake_token", token2)
}

func TestTokenManager_RefreshToken_Handles401(t *testing.T) {
	// 1. Criar um servidor mock que falha na primeira vez
	attempt := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt++
		if attempt == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"error": "invalid_credentials"}`)
			return
		}
		// Sucesso na segunda tentativa (não deve acontecer neste teste)
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"access_token": "should_not_get_this", "expires_in": 3600}`)
	}))
	defer server.Close()

	// 2. Criar o TokenManager
	logger, _ := zap.NewDevelopment()
	tm := NewTokenManager("bad_id", "bad_secret", "slug", "tenant", logger)
	tm.tokenURLOverride = server.URL

	// 3. Executar e validar
	_, err := tm.RefreshToken(context.Background())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "status 401")
}
