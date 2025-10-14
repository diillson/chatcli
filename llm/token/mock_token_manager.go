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

// SetRealm é a implementação mockada para o novo método da interface.
func (m *MockTokenManager) SetRealm(realm string) {
	m.Called(realm)
}
