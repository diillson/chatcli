/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package remote

import (
	"context"
	"crypto/tls"
	"encoding/pem"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	pb "github.com/diillson/chatcli/proto/chatcli/v1"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

type healthOnlyServer struct {
	pb.UnimplementedChatCLIServiceServer
}

func (healthOnlyServer) Health(context.Context, *pb.HealthRequest) (*pb.HealthResponse, error) {
	return &pb.HealthResponse{Status: pb.HealthResponse_SERVING, Version: "test"}, nil
}

// startPrivateCATLSServer serves gRPC over TLS with a certificate no
// system trust store knows, and returns its address and CA file.
func startPrivateCATLSServer(t *testing.T) (string, string) {
	t.Helper()
	// httptest mints a throwaway self-signed certificate for 127.0.0.1.
	certSrc := httptest.NewUnstartedServer(http.NotFoundHandler())
	certSrc.StartTLS()
	cert := certSrc.TLS.Certificates[0]
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certSrc.Certificate().Raw})
	certSrc.Close()

	caFile := filepath.Join(t.TempDir(), "ca.crt")
	if err := os.WriteFile(caFile, caPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	creds := credentials.NewTLS(&tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13})
	srv := grpc.NewServer(grpc.Creds(creds))
	pb.RegisterChatCLIServiceServer(srv, healthOnlyServer{})
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	return lis.Addr().String(), caFile
}

// --ca-cert implies TLS: with the CA alone (no --tls) the client dials TLS
// and trusts the private CA, even where plaintext is allowed.
func TestNewClient_CACertImpliesTLS(t *testing.T) {
	t.Setenv("CHATCLI_ALLOW_INSECURE", "true")
	t.Setenv("CHATCLI_TLS_CLIENT_CERT", "")
	addr, caFile := startPrivateCATLSServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	c, err := NewClient(ctx, Config{Address: addr, CertFile: caFile}, zap.NewNop())
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer func() { _ = c.Close() }()
	healthy, _, err := c.Health(ctx)
	if err != nil || !healthy {
		t.Fatalf("health over TLS with the private CA: healthy=%v err=%v", healthy, err)
	}
}

// The CA file is read even without --tls, so a wrong path is reported
// instead of being ignored.
func TestNewClient_CACertIsReadWithoutTLSFlag(t *testing.T) {
	_, err := NewClient(context.Background(), Config{Address: "127.0.0.1:1", CertFile: filepath.Join(t.TempDir(), "missing.crt")}, zap.NewNop())
	if err == nil || !strings.Contains(err.Error(), "CA certificate") {
		t.Fatalf("expected a CA read error, got %v", err)
	}
}
