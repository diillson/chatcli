/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cmd

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	"github.com/diillson/chatcli/i18n"
)

// testCert is a certificate and key, PEM-encoded, plus what signed it.
type testCert struct {
	certPEM, keyPEM []byte
	cert            *x509.Certificate
	key             *ecdsa.PrivateKey
}

// issueCert makes a certificate for the given names, signed by parent (or
// self-signed when parent is nil), valid from notBefore to notAfter.
func issueCert(t *testing.T, parent *testCert, isCA bool, dns []string, ips []net.IP, notBefore, notAfter time.Time) *testCert {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, _ := rand.Int(rand.Reader, big.NewInt(1<<62))
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "chatcli-test"},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		DNSNames:              dns,
		IPAddresses:           ips,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		IsCA:                  isCA,
	}
	signer, signerKey := tmpl, key
	if parent != nil {
		signer, signerKey = parent.cert, parent.key
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, signer, &key.PublicKey, signerKey)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	keyDER, _ := x509.MarshalECPrivateKey(key)
	return &testCert{
		certPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		keyPEM:  pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}),
		cert:    cert,
		key:     key,
	}
}

func validCert(t *testing.T, parent *testCert, isCA bool) *testCert {
	return issueCert(t, parent, isCA, []string{"chatcli.test"}, nil, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
}

func (c *testCert) write(t *testing.T, name string) (certPath, keyPath string) {
	t.Helper()
	dir := t.TempDir()
	certPath, keyPath = filepath.Join(dir, name+".crt"), filepath.Join(dir, name+".key")
	if err := os.WriteFile(certPath, c.certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, c.keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	return certPath, keyPath
}

// startHealthServer serves grpc.health.v1 on loopback with the given
// options and reports the whole server as status.
func startHealthServer(t *testing.T, status healthpb.HealthCheckResponse_ServingStatus, opts ...grpc.ServerOption) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	gs := grpc.NewServer(opts...)
	hs := health.NewServer()
	hs.SetServingStatus("", status)
	healthpb.RegisterHealthServer(gs, hs)
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(gs.Stop)
	return lis.Addr().String()
}

func tlsServer(t *testing.T, server *testCert, clientCA *testCert) grpc.ServerOption {
	t.Helper()
	pair, err := tls.X509KeyPair(server.certPEM, server.keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS13}
	if clientCA != nil {
		pool := x509.NewCertPool()
		pool.AddCert(clientCA.cert)
		cfg.ClientCAs = pool
		cfg.ClientAuth = tls.RequireAndVerifyClientCert
	}
	return grpc.Creds(credentials.NewTLS(cfg))
}

func healthcheckCLI(t *testing.T, args ...string) (code int, out, errOut string) {
	t.Helper()
	i18n.Init()
	var stdout, stderr bytes.Buffer
	code = RunHealthcheck(append([]string{"-timeout", "3s"}, args...), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func clearServerTLSEnv(t *testing.T) {
	// No real process listing: only what a test fakes is visible.
	old := procRoot
	procRoot = t.TempDir()
	t.Cleanup(func() { procRoot = old })
	for _, k := range []string{"CHATCLI_SERVER_TLS_CERT", "CHATCLI_SERVER_TLS_CLIENT_CA", "CHATCLI_TLS_CLIENT_CERT", "CHATCLI_TLS_CLIENT_KEY", "CHATCLI_SERVER_PORT", "CHATCLI_BIND_ADDRESS"} {
		t.Setenv(k, "")
	}
}

func TestHealthcheckPlaintext(t *testing.T) {
	clearServerTLSEnv(t)
	addr := startHealthServer(t, healthpb.HealthCheckResponse_SERVING)
	if code, out, errOut := healthcheckCLI(t, "-addr", addr); code != healthcheckServing || out == "" {
		t.Fatalf("serving server: code %d, out %q, err %q", code, out, errOut)
	}

	draining := startHealthServer(t, healthpb.HealthCheckResponse_NOT_SERVING)
	if code, _, errOut := healthcheckCLI(t, "-addr", draining); code != healthcheckNotServing || !strings.Contains(errOut, "NOT_SERVING") {
		t.Fatalf("draining server: code %d, err %q", code, errOut)
	}

	// A service the server does not report is an error, not a pass.
	if code, _, _ := healthcheckCLI(t, "-addr", addr, "-service", "nope.v1.Missing"); code != healthcheckNotServing {
		t.Fatalf("unknown service: code %d", code)
	}
}

func TestHealthcheckNoServer(t *testing.T) {
	clearServerTLSEnv(t)
	lis, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := lis.Addr().String()
	_ = lis.Close()
	if code, _, _ := healthcheckCLI(t, "-addr", addr, "-timeout", "500ms"); code != healthcheckNotServing {
		t.Fatalf("closed port: code %d", code)
	}
}

// With TLS the probe trusts exactly the server's own certificate, verified
// by its name: no CA to provision, no verification skipped.
func TestHealthcheckTLSTrustsTheServersOwnCertificate(t *testing.T) {
	clearServerTLSEnv(t)
	server := validCert(t, nil, false)
	certPath, _ := server.write(t, "server")
	addr := startHealthServer(t, healthpb.HealthCheckResponse_SERVING, tlsServer(t, server, nil))

	t.Setenv("CHATCLI_SERVER_TLS_CERT", certPath) // what the server's container already sets
	if code, _, errOut := healthcheckCLI(t, "-addr", addr); code != healthcheckServing {
		t.Fatalf("TLS server: code %d, err %q", code, errOut)
	}

	// Without TLS configured the probe speaks plaintext and must fail.
	t.Setenv("CHATCLI_SERVER_TLS_CERT", "")
	if code, _, _ := healthcheckCLI(t, "-addr", addr); code != healthcheckNotServing {
		t.Fatalf("plaintext against TLS: code %d", code)
	}
}

func TestHealthcheckTLSRejectsAnotherCertificate(t *testing.T) {
	clearServerTLSEnv(t)
	server := validCert(t, nil, false)
	addr := startHealthServer(t, healthpb.HealthCheckResponse_SERVING, tlsServer(t, server, nil))

	other, _ := validCert(t, nil, false).write(t, "other")
	if code, _, _ := healthcheckCLI(t, "-addr", addr, "-tls-cert", other); code != healthcheckNotServing {
		t.Fatalf("a server presenting another certificate must fail, got %d", code)
	}
}

// An expired certificate fails every client, so it fails the probe too.
func TestHealthcheckTLSRejectsAnExpiredCertificate(t *testing.T) {
	clearServerTLSEnv(t)
	expired := issueCert(t, nil, false, []string{"chatcli.test"}, nil, time.Now().Add(-2*time.Hour), time.Now().Add(-time.Hour))
	certPath, _ := expired.write(t, "expired")
	addr := startHealthServer(t, healthpb.HealthCheckResponse_SERVING, tlsServer(t, expired, nil))
	if code, _, _ := healthcheckCLI(t, "-addr", addr, "-tls-cert", certPath); code != healthcheckNotServing {
		t.Fatalf("expired certificate: code %d", code)
	}
}

func TestHealthcheckMutualTLS(t *testing.T) {
	clearServerTLSEnv(t)
	ca := validCert(t, nil, true)
	server := validCert(t, nil, false)
	certPath, _ := server.write(t, "server")
	caPath, _ := ca.write(t, "ca")
	addr := startHealthServer(t, healthpb.HealthCheckResponse_SERVING, tlsServer(t, server, ca))

	t.Setenv("CHATCLI_SERVER_TLS_CERT", certPath)
	t.Setenv("CHATCLI_SERVER_TLS_CLIENT_CA", caPath)
	code, _, errOut := healthcheckCLI(t, "-addr", addr)
	if code != healthcheckNotServing || !strings.Contains(errOut, "CHATCLI_TLS_CLIENT_CERT") {
		t.Fatalf("mTLS without a client pair must say what is missing: code %d, err %q", code, errOut)
	}

	clientCert, clientKey := validCert(t, ca, false).write(t, "client")
	t.Setenv("CHATCLI_TLS_CLIENT_CERT", clientCert)
	t.Setenv("CHATCLI_TLS_CLIENT_KEY", clientKey)
	if code, _, errOut := healthcheckCLI(t, "-addr", addr); code != healthcheckServing {
		t.Fatalf("mTLS with a client pair: code %d, err %q", code, errOut)
	}
}

func TestCertificateName(t *testing.T) {
	ipOnly := issueCert(t, nil, false, nil, []net.IP{net.ParseIP("10.0.0.7")}, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	if name, err := certificateName(ipOnly.certPEM); err != nil || name != "10.0.0.7" {
		t.Errorf("IP-only certificate: %q, %v", name, err)
	}
	noSAN := issueCert(t, nil, false, nil, nil, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	if _, err := certificateName(noSAN.certPEM); err == nil {
		t.Error("a certificate without names must be reported")
	}
}

func TestHealthcheckUsage(t *testing.T) {
	clearServerTLSEnv(t)
	if code, _, _ := healthcheckCLI(t, "-h"); code != healthcheckServing {
		t.Errorf("-h: code %d", code)
	}
	if code, _, _ := healthcheckCLI(t, "extra"); code != healthcheckUsage {
		t.Errorf("stray argument: code %d", code)
	}
	if code, _, _ := healthcheckCLI(t, "-timeout", "0s"); code != healthcheckUsage {
		t.Errorf("zero timeout: code %d", code)
	}
}

// fakeProcess lists a process under the fake proc root.
func fakeProcess(t *testing.T, pid string, argv ...string) {
	t.Helper()
	dir := filepath.Join(procRoot, pid)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cmdline"), []byte(strings.Join(argv, "\x00")+"\x00"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// Without a running server, the defaults are the server's variables.
func TestHealthcheckDefaultsFollowTheServerVariables(t *testing.T) {
	clearServerTLSEnv(t)
	t.Setenv("CHATCLI_SERVER_PORT", "6123")
	t.Setenv("CHATCLI_SERVER_TLS_CERT", "/env/server.crt")
	opts, err := parseHealthcheckFlags(nil, &bytes.Buffer{})
	if err != nil || opts.addr != "127.0.0.1:6123" || opts.serverCert != "/env/server.crt" {
		t.Fatalf("defaults: %+v, %v", opts, err)
	}
}

// The operator configures the server through flags: those of the running
// server win over the variables, wherever the server sits in the process
// list (PID 1, or under an init).
func TestHealthcheckDefaultsFollowTheRunningServersFlags(t *testing.T) {
	clearServerTLSEnv(t)
	t.Setenv("CHATCLI_SERVER_PORT", "6123")
	fakeProcess(t, "1", "/sbin/tini", "--", "chatcli", "server")
	fakeProcess(t, "7", "/usr/local/bin/chatcli", "server", "--port", "7001",
		"--tls-cert", "/etc/chatcli/tls/tls.crt", "--tls-key", "/etc/chatcli/tls/tls.key",
		"--tls-client-ca", "/etc/chatcli/client-ca/ca.crt")
	fakeProcess(t, "9", "/usr/local/bin/chatcli", "healthcheck")
	fakeProcess(t, "self", "chatcli", "server", "--port", "1")

	opts, err := parseHealthcheckFlags(nil, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if opts.addr != "127.0.0.1:7001" || opts.serverCert != "/etc/chatcli/tls/tls.crt" || opts.clientCA != "/etc/chatcli/client-ca/ca.crt" {
		t.Fatalf("running server's flags not followed: %+v", opts)
	}

	// Explicit flags still win.
	opts, _ = parseHealthcheckFlags([]string{"-addr", "10.0.0.9:50051", "-tls-cert", "/x.crt"}, &bytes.Buffer{})
	if opts.addr != "10.0.0.9:50051" || opts.serverCert != "/x.crt" {
		t.Fatalf("explicit flags lost: %+v", opts)
	}
}

// Flags the server itself would reject mean no server is running with them.
func TestHealthcheckIgnoresUnparsableServerFlags(t *testing.T) {
	clearServerTLSEnv(t)
	t.Setenv("CHATCLI_SERVER_PORT", "6123")
	fakeProcess(t, "1", "chatcli", "server", "--no-such-flag")
	opts, _ := parseHealthcheckFlags(nil, &bytes.Buffer{})
	if opts.addr != "127.0.0.1:6123" {
		t.Fatalf("addr %q, want the variables' answer", opts.addr)
	}
}

func TestDialHost(t *testing.T) {
	for bind, want := range map[string]string{
		"": "127.0.0.1", "0.0.0.0": "127.0.0.1", "127.0.0.1": "127.0.0.1",
		"::": "::1", "[::1]": "::1", "10.1.2.3": "10.1.2.3", "[fd00::5]": "fd00::5",
	} {
		if got := dialHost(bind); got != want {
			t.Errorf("dialHost(%q) = %q, want %q", bind, got, want)
		}
	}
	clearServerTLSEnv(t)
	t.Setenv("CHATCLI_BIND_ADDRESS", "10.1.2.3")
	if opts, _ := parseHealthcheckFlags(nil, &bytes.Buffer{}); opts.addr != "10.1.2.3:50051" {
		t.Errorf("specific bind: addr %q", opts.addr)
	}
}
