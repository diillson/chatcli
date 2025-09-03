package token

import (
	"context"

	"github.com/stretchr/testify/mock"
)

// Garante que o mock implementa a interface em tempo de compilação
var _ Manager = (*MockTokenManager)(nil)

type MockTokenManager struct {
	mock.Mock
}

func (m *MockTokenManager) GetAccessToken(ctx context.Context) (string, error) {
	args := m.Called(ctx)
	return args.String(0), args.Error(1)
}

func (m *MockTokenManager) RefreshToken(ctx context.Context) (string, error) {
	args := m.Called(ctx)
	return args.String(0), args.Error(1)
}

func (m *MockTokenManager) GetSlugAndTenantName() (string, string) {
	args := m.Called()
	return args.String(0), args.String(1)
}

func (m *MockTokenManager) SetSlugAndTenantName(slug, tenant string) {
	m.Called(slug, tenant)
}
