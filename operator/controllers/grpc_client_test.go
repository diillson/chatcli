/*
 * ChatCLI - Kubernetes Operator
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package controllers

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
)

// writeTestCACert writes a self-signed CA certificate PEM to dir and
// returns its path.
func writeTestCACert(t *testing.T, dir string) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "chatcli-test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	path := filepath.Join(dir, "ca.pem")
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.WriteFile(path, pemBytes, 0o600); err != nil {
		t.Fatalf("write CA cert: %v", err)
	}
	return path
}

// clearGRPCTLSEnv isolates each test from ambient TLS configuration.
func clearGRPCTLSEnv(t *testing.T) {
	t.Helper()
	t.Setenv("CHATCLI_GRPC_TLS_CA", "")
	t.Setenv("CHATCLI_GRPC_TLS_CERT", "")
	t.Setenv("CHATCLI_GRPC_TLS_KEY", "")
}

func TestServerClientConnect_RelativeCAPathRejected(t *testing.T) {
	clearGRPCTLSEnv(t)
	t.Setenv("CHATCLI_GRPC_TLS_CA", filepath.Join("relative", "ca.pem"))

	sc := NewServerClient(zap.NewNop())
	err := sc.Connect("localhost:19999", ConnectionOpts{})
	if err == nil || !strings.Contains(err.Error(), "must be an absolute path") {
		t.Fatalf("expected absolute-path error, got %v", err)
	}
}

func TestServerClientConnect_MissingCAFile(t *testing.T) {
	clearGRPCTLSEnv(t)
	t.Setenv("CHATCLI_GRPC_TLS_CA", filepath.Join(t.TempDir(), "absent.pem"))

	sc := NewServerClient(zap.NewNop())
	err := sc.Connect("localhost:19999", ConnectionOpts{})
	if err == nil || !strings.Contains(err.Error(), "failed to read CA cert") {
		t.Fatalf("expected read error, got %v", err)
	}
}

func TestServerClientConnect_InvalidCAContents(t *testing.T) {
	clearGRPCTLSEnv(t)
	path := filepath.Join(t.TempDir(), "garbage.pem")
	if err := os.WriteFile(path, []byte("not a certificate"), 0o600); err != nil {
		t.Fatalf("write garbage CA: %v", err)
	}
	t.Setenv("CHATCLI_GRPC_TLS_CA", path)

	sc := NewServerClient(zap.NewNop())
	err := sc.Connect("localhost:19999", ConnectionOpts{})
	if err == nil || !strings.Contains(err.Error(), "failed to parse CA certificate") {
		t.Fatalf("expected parse error, got %v", err)
	}
}

func TestServerClientConnect_ValidCAAndReconnect(t *testing.T) {
	clearGRPCTLSEnv(t)
	t.Setenv("CHATCLI_GRPC_TLS_CA", writeTestCACert(t, t.TempDir()))

	sc := NewServerClient(zap.NewNop())
	// grpc.NewClient is lazy, so a successful Connect only requires the
	// TLS material to load — no listening server needed.
	if err := sc.Connect("localhost:19999", ConnectionOpts{Token: "tok"}); err != nil {
		t.Fatalf("first Connect: %v", err)
	}
	if sc.conn == nil || sc.client == nil {
		t.Fatal("Connect must populate conn and client")
	}

	// A second Connect must tear down the previous connection and
	// succeed again.
	if err := sc.Connect("localhost:19998", ConnectionOpts{}); err != nil {
		t.Fatalf("reconnect: %v", err)
	}
	if sc.conn == nil {
		t.Fatal("reconnect must leave an active connection")
	}
	t.Cleanup(func() { _ = sc.conn.Close() })
}
