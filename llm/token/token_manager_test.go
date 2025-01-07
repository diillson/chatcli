package token

import (
	"context"
	"testing"
)

func TestTokenManager_GetAccessToken(t *testing.T) {
	manager := &MockTokenManager{
		accessToken: "mock_access_token",
		err:         nil,
	}

	ctx := context.Background()
	token, err := manager.GetAccessToken(ctx)
	if err != nil {
		t.Errorf("Erro inesperado: %v", err)
	}
	if token != "mock_access_token" {
		t.Errorf("Token inesperado: %s", token)
	}
}
