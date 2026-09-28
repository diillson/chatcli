/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package server

import (
	"context"
	"net"
	"testing"
	"time"

	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/test/bufconn"
)

// dialHealth serves srv's gRPC server over an in-memory listener and returns
// a grpc.health.v1 client that carries no credentials, like a kubelet probe.
func dialHealth(t *testing.T, srv *Server) healthpb.HealthClient {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	go func() { _ = srv.grpcServer.Serve(lis) }()
	t.Cleanup(srv.grpcServer.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return healthpb.NewHealthClient(conn)
}

func TestHealthService_AnswersProbesWithoutCredentials(t *testing.T) {
	t.Setenv("CHATCLI_HUB_ENABLED", "false")
	srv := New(Config{Token: "required-token"}, nil, nil, zap.NewNop())
	defer srv.Stop()
	client := dialHealth(t, srv)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Before the listener is marked serving, probes see NOT_SERVING.
	resp, err := client.Check(ctx, &healthpb.HealthCheckRequest{})
	if err != nil {
		t.Fatalf("health check without credentials was refused: %v", err)
	}
	if resp.GetStatus() != healthpb.HealthCheckResponse_NOT_SERVING {
		t.Fatalf("expected NOT_SERVING before start, got %v", resp.GetStatus())
	}

	setHealth(srv.health, healthpb.HealthCheckResponse_SERVING)
	for _, name := range healthServiceNames() {
		resp, err := client.Check(ctx, &healthpb.HealthCheckRequest{Service: name})
		if err != nil {
			t.Fatalf("health check for %q failed: %v", name, err)
		}
		if resp.GetStatus() != healthpb.HealthCheckResponse_SERVING {
			t.Fatalf("expected SERVING for %q, got %v", name, resp.GetStatus())
		}
	}

	// Shutdown flips the status before connections drain (Stop and the
	// signal handler both call this first).
	setHealth(srv.health, healthpb.HealthCheckResponse_NOT_SERVING)
	resp, err = client.Check(ctx, &healthpb.HealthCheckRequest{})
	if err != nil {
		t.Fatalf("health check during shutdown failed: %v", err)
	}
	if resp.GetStatus() != healthpb.HealthCheckResponse_NOT_SERVING {
		t.Fatalf("expected NOT_SERVING once shutdown begins, got %v", resp.GetStatus())
	}
}

func TestServerStop_IsIdempotent(t *testing.T) {
	t.Setenv("CHATCLI_HUB_ENABLED", "false")
	srv := New(Config{}, nil, nil, zap.NewNop())
	srv.Stop()
	srv.Stop()
}

func TestIsHealthMethod(t *testing.T) {
	for method, want := range map[string]bool{
		"/chatcli.v1.ChatCLIService/Health":     true,
		"/grpc.health.v1.Health/Check":          true,
		"/grpc.health.v1.Health/Watch":          true,
		"/grpc.health.v1.Health/List":           true,
		"/chatcli.v1.ChatCLIService/SendPrompt": false,
		"/evil.grpc.health.v1.Health/Check":     false,
	} {
		if got := isHealthMethod(method); got != want {
			t.Errorf("isHealthMethod(%q) = %v, want %v", method, got, want)
		}
	}
}
