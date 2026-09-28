/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package server

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// JWT material that is configured but cannot be loaded must never leave
// the server open. Before this guard, the bind guard counted the non-empty
// variable as a credential, configureJWT disabled JWT, and authorize then
// admitted every caller as an administrator.
func TestJWT_UnloadablePublicKey_DeniesInsteadOfAdmittingAdmin(t *testing.T) {
	t.Setenv("CHATCLI_JWT_PUBLIC_KEY", filepath.Join(t.TempDir(), "missing.pem"))

	ai := NewTokenAuthInterceptor("", zap.NewNop())
	ctx, err := ai.authorize(context.Background(), "/chatcli.v1.ChatCLIService/SendPrompt")
	if err == nil {
		t.Fatalf("unloadable RS256 key admitted the caller as %+v", UserFromContext(ctx))
	}
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("expected Unauthenticated, got %v", err)
	}
	if ai.jwtConfigError() == nil {
		t.Fatal("the load failure must be reported so the server refuses to start")
	}
}

func TestJWT_UnloadableKeyMaterialInSecret_DeniesInsteadOfAdmittingAdmin(t *testing.T) {
	t.Setenv("CHATCLI_JWT_SECRET", "-----BEGIN PUBLIC KEY-----\nnot-a-key\n-----END PUBLIC KEY-----\n")

	ai := NewTokenAuthInterceptor("", zap.NewNop())
	if _, err := ai.authorize(context.Background(), "/chatcli.v1.ChatCLIService/SendPrompt"); err == nil {
		t.Fatal("unloadable RSA material in CHATCLI_JWT_SECRET admitted the caller")
	}
	if ai.jwtConfigError() == nil {
		t.Fatal("the load failure must be reported so the server refuses to start")
	}
}

// With JWT as the only credential, a broken key stops the server at start.
func TestJWT_UnloadablePublicKey_ServerRefusesToStart(t *testing.T) {
	t.Setenv("CHATCLI_JWT_PUBLIC_KEY", filepath.Join(t.TempDir(), "missing.pem"))
	t.Setenv("CHATCLI_BIND_ADDRESS", "127.0.0.1")
	t.Setenv("CHATCLI_HUB_ENABLED", "false")

	srv := New(Config{Port: 0, MetricsPort: 0}, nil, nil, zap.NewNop())
	defer srv.Stop()
	started := make(chan error, 1)
	go func() { started <- srv.Start() }()
	select {
	case err := <-started:
		if err == nil {
			t.Fatal("server started with JWT material it could not load")
		}
	case <-time.After(3 * time.Second):
		srv.grpcServer.Stop()
		t.Fatal("server is serving with JWT material it could not load")
	}
}

func TestJWT_ValidOrAbsentMaterial_ReportsNoConfigError(t *testing.T) {
	ai := NewTokenAuthInterceptor("tok", zap.NewNop())
	if err := ai.jwtConfigError(); err != nil {
		t.Fatalf("no JWT configured must not be an error: %v", err)
	}
}

// A broken key next to a shared token must not take the token down: that
// deployment was closed before and stays closed and serving. JWT callers
// are refused, because no JWT material is loaded to verify them.
func TestJWT_UnloadablePublicKeyWithToken_KeepsTokenWorking(t *testing.T) {
	t.Setenv("CHATCLI_JWT_PUBLIC_KEY", filepath.Join(t.TempDir(), "missing.pem"))

	ai := NewTokenAuthInterceptor("shared-token", zap.NewNop())
	if err := ai.jwtConfigError(); err != nil {
		t.Fatalf("a token-secured server must not refuse to start over the JWT key: %v", err)
	}

	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer shared-token"))
	if _, err := ai.authorize(ctx, "/chatcli.v1.ChatCLIService/SendPrompt"); err != nil {
		t.Fatalf("shared token rejected next to a broken JWT key: %v", err)
	}

	anon := context.Background()
	if _, err := ai.authorize(anon, "/chatcli.v1.ChatCLIService/SendPrompt"); err == nil {
		t.Fatal("anonymous caller admitted")
	}
}
