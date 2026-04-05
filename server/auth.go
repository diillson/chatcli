/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package server

import (
	"context"
	"crypto/subtle"
	"strings"

	"github.com/diillson/chatcli/i18n"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// TokenAuthInterceptor validates Bearer tokens from gRPC metadata.
type TokenAuthInterceptor struct {
	token  string
	logger *zap.Logger
}

// NewTokenAuthInterceptor creates a new auth interceptor.
// If token is empty, authentication is disabled (all requests allowed).
func NewTokenAuthInterceptor(token string, logger *zap.Logger) *TokenAuthInterceptor {
	return &TokenAuthInterceptor{token: token, logger: logger}
}

// Unary returns a grpc.UnaryServerInterceptor that validates the Bearer token.
func (a *TokenAuthInterceptor) Unary() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		if err := a.authorize(ctx, info.FullMethod); err != nil {
			return nil, err
		}
		return handler(ctx, req)
	}
}

// Stream returns a grpc.StreamServerInterceptor that validates the Bearer token.
func (a *TokenAuthInterceptor) Stream() grpc.StreamServerInterceptor {
	return func(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if err := a.authorize(ss.Context(), info.FullMethod); err != nil {
			return err
		}
		return handler(srv, ss)
	}
}

func (a *TokenAuthInterceptor) authorize(ctx context.Context, method string) error {
	// Skip auth if no token is configured
	if a.token == "" {
		return nil
	}

	// Always allow health checks without auth
	if strings.HasSuffix(method, "/Health") {
		return nil
	}

	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		a.logger.Warn(i18n.T("server.auth.metadata_warn"), zap.String("method", method))
		return status.Errorf(codes.Unauthenticated, "%s", i18n.T("server.auth.missing_metadata"))
	}

	values := md.Get("authorization")
	if len(values) == 0 {
		a.logger.Warn(i18n.T("server.auth.authorization_warn"), zap.String("method", method))
		return status.Errorf(codes.Unauthenticated, "%s", i18n.T("server.auth.missing_authorization"))
	}

	token := values[0]
	if !strings.HasPrefix(token, "Bearer ") {
		a.logger.Warn(i18n.T("server.auth.format_warn"), zap.String("method", method))
		return status.Errorf(codes.Unauthenticated, "%s", i18n.T("server.auth.invalid_format"))
	}

	token = strings.TrimPrefix(token, "Bearer ")
	if subtle.ConstantTimeCompare([]byte(token), []byte(a.token)) != 1 {
		a.logger.Warn(i18n.T("server.auth.token_warn"), zap.String("method", method))
		return status.Errorf(codes.PermissionDenied, "%s", i18n.T("server.auth.invalid_token"))
	}

	return nil
}
