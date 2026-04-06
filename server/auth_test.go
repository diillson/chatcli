package server

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestTokenAuthInterceptor_NoTokenConfigured(t *testing.T) {
	logger := zap.NewNop()
	auth := NewTokenAuthInterceptor("", logger)

	// With no token configured, all requests should pass and inject default admin user
	ctx := context.Background()
	newCtx, err := auth.authorize(ctx, "/chatcli.v1.ChatCLIService/SendPrompt")
	assert.NoError(t, err)
	user := UserFromContext(newCtx)
	assert.NotNil(t, user)
	assert.Equal(t, RoleAdmin, user.Role)
}

func TestTokenAuthInterceptor_HealthAlwaysAllowed(t *testing.T) {
	logger := zap.NewNop()
	auth := NewTokenAuthInterceptor("secret123", logger)

	// Health endpoint should always be allowed even without auth
	ctx := context.Background()
	_, err := auth.authorize(ctx, "/chatcli.v1.ChatCLIService/Health")
	assert.NoError(t, err)
}

func TestTokenAuthInterceptor_MissingMetadata(t *testing.T) {
	logger := zap.NewNop()
	auth := NewTokenAuthInterceptor("secret123", logger)

	ctx := context.Background()
	_, err := auth.authorize(ctx, "/chatcli.v1.ChatCLIService/SendPrompt")

	assert.Error(t, err)
	s, ok := status.FromError(err)
	assert.True(t, ok)
	assert.Equal(t, codes.Unauthenticated, s.Code())
}

func TestTokenAuthInterceptor_MissingAuthHeader(t *testing.T) {
	logger := zap.NewNop()
	auth := NewTokenAuthInterceptor("secret123", logger)

	md := metadata.New(map[string]string{"other-key": "value"})
	ctx := metadata.NewIncomingContext(context.Background(), md)
	_, err := auth.authorize(ctx, "/chatcli.v1.ChatCLIService/SendPrompt")

	assert.Error(t, err)
	s, ok := status.FromError(err)
	assert.True(t, ok)
	assert.Equal(t, codes.Unauthenticated, s.Code())
}

func TestTokenAuthInterceptor_InvalidFormat(t *testing.T) {
	logger := zap.NewNop()
	auth := NewTokenAuthInterceptor("secret123", logger)

	md := metadata.New(map[string]string{"authorization": "Token secret123"})
	ctx := metadata.NewIncomingContext(context.Background(), md)
	_, err := auth.authorize(ctx, "/chatcli.v1.ChatCLIService/SendPrompt")

	assert.Error(t, err)
	s, ok := status.FromError(err)
	assert.True(t, ok)
	assert.Equal(t, codes.Unauthenticated, s.Code())
}

func TestTokenAuthInterceptor_InvalidToken(t *testing.T) {
	logger := zap.NewNop()
	auth := NewTokenAuthInterceptor("secret123", logger)

	md := metadata.New(map[string]string{"authorization": "Bearer wrongtoken"})
	ctx := metadata.NewIncomingContext(context.Background(), md)
	_, err := auth.authorize(ctx, "/chatcli.v1.ChatCLIService/SendPrompt")

	assert.Error(t, err)
	s, ok := status.FromError(err)
	assert.True(t, ok)
	// Security (M8): All auth failures return Unauthenticated (no info leakage)
	assert.Equal(t, codes.Unauthenticated, s.Code())
}

func TestTokenAuthInterceptor_ValidToken(t *testing.T) {
	logger := zap.NewNop()
	auth := NewTokenAuthInterceptor("secret123", logger)

	md := metadata.New(map[string]string{"authorization": "Bearer secret123"})
	ctx := metadata.NewIncomingContext(context.Background(), md)
	newCtx, err := auth.authorize(ctx, "/chatcli.v1.ChatCLIService/SendPrompt")

	assert.NoError(t, err)
	// Legacy token gets admin role
	user := UserFromContext(newCtx)
	assert.NotNil(t, user)
	assert.Equal(t, RoleAdmin, user.Role)
}

func TestTokenAuthInterceptor_ConstantTimeComparison(t *testing.T) {
	logger := zap.NewNop()
	auth := NewTokenAuthInterceptor("secret123", logger)

	// All of these must be rejected with Unauthenticated,
	// confirming the constant-time comparison path is reached.
	testCases := []struct {
		name  string
		token string
	}{
		{"partial prefix match", "secret"},
		{"partial with extra", "secret1234"},
		{"different length shorter", "sec"},
		{"different length longer", "secret123secret123secret123"},
		{"completely different", "totallyWrongToken"},
		{"empty token", ""},
		{"case mismatch", "Secret123"},
		{"null byte injection", "secret\x00123"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			md := metadata.New(map[string]string{"authorization": "Bearer " + tc.token})
			ctx := metadata.NewIncomingContext(context.Background(), md)
			_, err := auth.authorize(ctx, "/chatcli.v1.ChatCLIService/SendPrompt")

			assert.Error(t, err)
			s, ok := status.FromError(err)
			assert.True(t, ok)
			// Security (M8): Normalized to Unauthenticated for all failures
			assert.Equal(t, codes.Unauthenticated, s.Code())
		})
	}
}
