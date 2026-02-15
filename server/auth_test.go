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

	// With no token configured, all requests should pass
	ctx := context.Background()
	err := auth.authorize(ctx, "/chatcli.v1.ChatCLIService/SendPrompt")
	assert.NoError(t, err)
}

func TestTokenAuthInterceptor_HealthAlwaysAllowed(t *testing.T) {
	logger := zap.NewNop()
	auth := NewTokenAuthInterceptor("secret123", logger)

	// Health endpoint should always be allowed even without auth
	ctx := context.Background()
	err := auth.authorize(ctx, "/chatcli.v1.ChatCLIService/Health")
	assert.NoError(t, err)
}

func TestTokenAuthInterceptor_MissingMetadata(t *testing.T) {
	logger := zap.NewNop()
	auth := NewTokenAuthInterceptor("secret123", logger)

	ctx := context.Background()
	err := auth.authorize(ctx, "/chatcli.v1.ChatCLIService/SendPrompt")

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
	err := auth.authorize(ctx, "/chatcli.v1.ChatCLIService/SendPrompt")

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
	err := auth.authorize(ctx, "/chatcli.v1.ChatCLIService/SendPrompt")

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
	err := auth.authorize(ctx, "/chatcli.v1.ChatCLIService/SendPrompt")

	assert.Error(t, err)
	s, ok := status.FromError(err)
	assert.True(t, ok)
	assert.Equal(t, codes.PermissionDenied, s.Code())
}

func TestTokenAuthInterceptor_ValidToken(t *testing.T) {
	logger := zap.NewNop()
	auth := NewTokenAuthInterceptor("secret123", logger)

	md := metadata.New(map[string]string{"authorization": "Bearer secret123"})
	ctx := metadata.NewIncomingContext(context.Background(), md)
	err := auth.authorize(ctx, "/chatcli.v1.ChatCLIService/SendPrompt")

	assert.NoError(t, err)
}
