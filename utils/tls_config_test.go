/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package utils

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/zap"
)

// writeTestCABundle generates a self-signed CA and writes it as PEM,
// returning the file path.
func writeTestCABundle(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "chatcli-test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	path := filepath.Join(t.TempDir(), "ca.pem")
	out := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.WriteFile(path, out, 0o600); err != nil {
		t.Fatalf("write bundle: %v", err)
	}
	return path
}

func TestNewTLSConfigFromEnvUnset(t *testing.T) {
	t.Setenv(envCABundle, "")
	t.Setenv(envInsecureSkipVerify, "")
	if cfg := newTLSConfigFromEnv(zap.NewNop()); cfg != nil {
		t.Fatalf("expected nil config when no override is set, got %+v", cfg)
	}
}

func TestNewTLSConfigFromEnvInsecure(t *testing.T) {
	t.Setenv(envCABundle, "")
	t.Setenv(envInsecureSkipVerify, "TRUE") // case-insensitive, like the Bedrock knob
	cfg := newTLSConfigFromEnv(zap.NewNop())
	if cfg == nil || !cfg.InsecureSkipVerify {
		t.Fatalf("expected InsecureSkipVerify=true, got %+v", cfg)
	}
	if cfg.MinVersion != tls.VersionTLS12 {
		t.Fatalf("expected MinVersion TLS1.2, got %d", cfg.MinVersion)
	}
}

func TestNewTLSConfigFromEnvCABundle(t *testing.T) {
	t.Setenv(envCABundle, writeTestCABundle(t))
	t.Setenv(envInsecureSkipVerify, "")
	cfg := newTLSConfigFromEnv(zap.NewNop())
	if cfg == nil || cfg.RootCAs == nil {
		t.Fatalf("expected RootCAs from bundle, got %+v", cfg)
	}
	if cfg.InsecureSkipVerify {
		t.Fatal("CA bundle must not disable verification")
	}
}

func TestNewTLSConfigFromEnvInsecureWinsOverBundle(t *testing.T) {
	t.Setenv(envCABundle, writeTestCABundle(t))
	t.Setenv(envInsecureSkipVerify, "true")
	cfg := newTLSConfigFromEnv(zap.NewNop())
	if cfg == nil || !cfg.InsecureSkipVerify {
		t.Fatalf("insecure must take precedence over the bundle, got %+v", cfg)
	}
}

func TestNewTLSConfigFromEnvBadBundleFailsOpen(t *testing.T) {
	t.Setenv(envInsecureSkipVerify, "")

	t.Setenv(envCABundle, filepath.Join(t.TempDir(), "missing.pem"))
	if cfg := newTLSConfigFromEnv(zap.NewNop()); cfg != nil {
		t.Fatalf("unreadable bundle must keep default trust, got %+v", cfg)
	}

	garbage := filepath.Join(t.TempDir(), "garbage.pem")
	if err := os.WriteFile(garbage, []byte("not a pem"), 0o600); err != nil {
		t.Fatalf("write garbage: %v", err)
	}
	t.Setenv(envCABundle, garbage)
	if cfg := newTLSConfigFromEnv(zap.NewNop()); cfg != nil {
		t.Fatalf("garbage bundle must keep default trust, got %+v", cfg)
	}
}

func TestApplyGlobalTLSTrustWiresClientsAndDefaultTransport(t *testing.T) {
	dt, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		t.Skip("http.DefaultTransport is not *http.Transport")
	}
	prevDefault := dt.TLSClientConfig
	prevGlobal := globalTLSConfig.Load()
	t.Cleanup(func() {
		dt.TLSClientConfig = prevDefault
		globalTLSConfig.Store(prevGlobal)
	})

	t.Setenv(envCABundle, writeTestCABundle(t))
	t.Setenv(envInsecureSkipVerify, "")
	ApplyGlobalTLSTrust(zap.NewNop())

	got := GlobalTLSConfig()
	if got == nil || got.RootCAs == nil {
		t.Fatalf("GlobalTLSConfig not populated: %+v", got)
	}
	if dt.TLSClientConfig == nil || dt.TLSClientConfig.RootCAs == nil {
		t.Fatal("http.DefaultTransport did not inherit the trust override")
	}

	client := NewHTTPClient(zap.NewNop(), time.Second)
	lt, ok := client.Transport.(*LoggingTransport)
	if !ok {
		t.Fatalf("unexpected outer transport %T", client.Transport)
	}
	inner, ok := lt.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("unexpected inner transport %T", lt.Transport)
	}
	if inner.TLSClientConfig == nil || inner.TLSClientConfig.RootCAs == nil {
		t.Fatal("NewHTTPClient did not inherit the trust override")
	}
}

func TestApplyGlobalTLSTrustNoOverridesIsNoOp(t *testing.T) {
	dt, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		t.Skip("http.DefaultTransport is not *http.Transport")
	}
	prevDefault := dt.TLSClientConfig
	prevGlobal := globalTLSConfig.Load()
	t.Cleanup(func() {
		dt.TLSClientConfig = prevDefault
		globalTLSConfig.Store(prevGlobal)
	})

	t.Setenv(envCABundle, "")
	t.Setenv(envInsecureSkipVerify, "")
	ApplyGlobalTLSTrust(zap.NewNop())

	if GlobalTLSConfig() != nil {
		t.Fatal("expected nil global config with no overrides")
	}
	if dt.TLSClientConfig != prevDefault {
		t.Fatal("DefaultTransport must not be touched when no override is set")
	}
}
