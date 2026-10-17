/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package server

import (
	"context"
	"net"
	"os"
	"syscall"
	"testing"
	"time"

	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/test/bufconn"
)

// serveHealth serves srv on one in-memory listener and returns a function
// that opens a new connection to it each time it is called.
func serveHealth(t *testing.T, srv *Server) func() healthpb.HealthClient {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	go func() { _ = srv.grpcServer.Serve(lis) }()
	return func() healthpb.HealthClient {
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
}

// During the drain the server reports NOT_SERVING but still accepts new
// connections and answers them; it stops only after the delay.
func TestDrainAndStop_ServesNewConnectionsDuringTheDelay(t *testing.T) {
	t.Setenv("CHATCLI_HUB_ENABLED", "false")
	const delay = 400 * time.Millisecond
	srv := New(Config{ShutdownDelay: delay}, nil, nil, zap.NewNop())
	defer srv.Stop()
	setHealth(srv.health, healthpb.HealthCheckResponse_SERVING)
	dial := serveHealth(t, srv)
	first := dial()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := first.Check(ctx, &healthpb.HealthCheckRequest{}); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	done := make(chan struct{})
	go func() { srv.drainAndStop(make(chan os.Signal)); close(done) }()

	// A connection opened after SIGTERM, as from a client that still
	// resolves the pod, is answered.
	time.Sleep(50 * time.Millisecond)
	late := dial()
	resp, err := late.Check(ctx, &healthpb.HealthCheckRequest{})
	if err != nil {
		t.Fatalf("a new connection during the drain was refused: %v", err)
	}
	if resp.GetStatus() != healthpb.HealthCheckResponse_NOT_SERVING {
		t.Fatalf("health during the drain = %v, want NOT_SERVING", resp.GetStatus())
	}

	select {
	case <-done:
		t.Fatal("the server stopped before the drain delay")
	case <-time.After(delay / 2):
	}
	<-done
	if elapsed := time.Since(start); elapsed < delay {
		t.Fatalf("stopped after %v, want at least %v", elapsed, delay)
	}
}

// A second signal ends the drain at once.
func TestDrainAndStop_SecondSignalCutsTheDelayShort(t *testing.T) {
	t.Setenv("CHATCLI_HUB_ENABLED", "false")
	srv := New(Config{ShutdownDelay: time.Hour}, nil, nil, zap.NewNop())
	defer srv.Stop()
	interrupt := make(chan os.Signal, 1)
	interrupt <- syscall.SIGTERM
	done := make(chan struct{})
	go func() { srv.drainAndStop(interrupt); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a second signal did not end the drain")
	}
}

// Without a delay the server stops at once, as before.
func TestDrainAndStop_NoDelayStopsAtOnce(t *testing.T) {
	t.Setenv("CHATCLI_HUB_ENABLED", "false")
	srv := New(Config{}, nil, nil, zap.NewNop())
	defer srv.Stop()
	done := make(chan struct{})
	go func() { srv.drainAndStop(make(chan os.Signal)); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the server did not stop without a delay")
	}
	if got := srv.health; got == nil {
		t.Fatal("no health server")
	}
}
