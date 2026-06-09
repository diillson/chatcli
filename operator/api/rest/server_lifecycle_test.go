/*
 * ChatCLI - Kubernetes Operator
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package rest

import (
	"context"
	"testing"
	"time"
)

func TestNewAPIServer_UsesAuthHeaderConstant(t *testing.T) {
	s := NewAPIServer(nil, "127.0.0.1:0")
	if s.apiKeyHeader != authHeaderName {
		t.Errorf("apiKeyHeader = %q, want %q", s.apiKeyHeader, authHeaderName)
	}
}

func TestAPIServerStart_ShutsDownOnContextCancel(t *testing.T) {
	t.Setenv("CHATCLI_AIOPS_TLS_CERT", "")
	t.Setenv("CHATCLI_AIOPS_TLS_KEY", "")

	s := NewAPIServer(nil, "127.0.0.1:0")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Start(ctx) }()

	// Give ListenAndServe a moment to bind before triggering shutdown.
	time.Sleep(200 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Start returned error on graceful shutdown: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not shut down after context cancellation")
	}
}
