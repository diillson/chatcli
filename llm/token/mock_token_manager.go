package token

import (
	"context"
)

// MockTokenManager é um mock que implementa o TokenManager
type MockTokenManager struct {
	accessToken string
	err         error
}

func (m *MockTokenManager) GetAccessToken(ctx context.Context) (string, error) {
	return m.accessToken, m.err
}

func (m *MockTokenManager) RefreshToken(ctx context.Context) (string, error) {
	// Retorna o mesmo token para simplificar
	return m.accessToken, m.err
}

func (m *MockTokenManager) GetSlugAndTenantName() (string, string) {
	return "mock_slug", "mock_tenant"
}

func (m *MockTokenManager) SetSlugAndTenantName(slug, tenant string) {
	// Não faz nada no mock
}
