/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package server

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/zap"
)

// --- helpers ---

func b64url(b []byte) string {
	return base64.RawURLEncoding.EncodeToString(b)
}

func mintUnsigned(t *testing.T, alg string, claims map[string]interface{}) (signingInput string) {
	t.Helper()
	header, err := json.Marshal(map[string]string{"alg": alg, "typ": "JWT"})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	return b64url(header) + "." + b64url(payload)
}

func mintHS256(t *testing.T, secret []byte, claims map[string]interface{}) string {
	t.Helper()
	in := mintUnsigned(t, "HS256", claims)
	return in + "." + b64url(computeHS256(in, secret))
}

func mintRS256(t *testing.T, key *rsa.PrivateKey, claims map[string]interface{}) string {
	t.Helper()
	in := mintUnsigned(t, "RS256", claims)
	digest := sha256.Sum256([]byte(in))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return in + "." + b64url(sig)
}

func writeRSAPublicKeyPEM(t *testing.T, dir string, pub *rsa.PublicKey) string {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "jwt.pub")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func validClaims() map[string]interface{} {
	return map[string]interface{}{
		"sub":  "alice",
		"role": "admin",
		"exp":  float64(time.Now().Add(time.Hour).Unix()),
	}
}

// --- RS256 ---

func TestJWT_RS256_AcceptsTokenSignedByTrustedKey(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("CHATCLI_JWT_PUBLIC_KEY", writeRSAPublicKeyPEM(t, t.TempDir(), &key.PublicKey))

	ai := NewTokenAuthInterceptor("", zap.NewNop())
	if ai.jwtAlg != jwtAlgRS256 {
		t.Fatalf("expected RS256 to be configured, got %q", ai.jwtAlg)
	}

	user, err := ai.validateJWT(mintRS256(t, key, validClaims()))
	if err != nil {
		t.Fatalf("valid RS256 token rejected: %v", err)
	}
	if user.Subject != "alice" || user.Role != RoleAdmin {
		t.Fatalf("unexpected identity: %+v", user)
	}
}

func TestJWT_RS256_RejectsTokenFromAnotherKey(t *testing.T) {
	trusted, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	attacker, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("CHATCLI_JWT_PUBLIC_KEY", writeRSAPublicKeyPEM(t, t.TempDir(), &trusted.PublicKey))

	ai := NewTokenAuthInterceptor("", zap.NewNop())
	if _, err := ai.validateJWT(mintRS256(t, attacker, validClaims())); err == nil {
		t.Fatal("token signed by an untrusted key was accepted")
	}
}

// The forgery that makes a naive RS256 implementation worse than no RS256
// at all: the RSA public key is public, so if the verifier lets the token
// pick the algorithm, anyone can mint an HS256 token using that public key
// as the shared secret.
func TestJWT_RS256_RefusesAlgorithmConfusion(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	pubPath := writeRSAPublicKeyPEM(t, dir, &key.PublicKey)
	t.Setenv("CHATCLI_JWT_PUBLIC_KEY", pubPath)

	pubPEM, err := os.ReadFile(pubPath)
	if err != nil {
		t.Fatal(err)
	}

	ai := NewTokenAuthInterceptor("", zap.NewNop())
	forged := mintHS256(t, pubPEM, validClaims())
	if _, err := ai.validateJWT(forged); err == nil {
		t.Fatal("HS256 token signed with the RSA public key was accepted — algorithm confusion")
	}
}

func TestJWT_HS256_RefusesRS256Token(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("CHATCLI_JWT_SECRET", "a-plain-shared-secret")

	ai := NewTokenAuthInterceptor("", zap.NewNop())
	if ai.jwtAlg != jwtAlgHS256 {
		t.Fatalf("expected HS256, got %q", ai.jwtAlg)
	}
	if _, err := ai.validateJWT(mintRS256(t, key, validClaims())); err == nil {
		t.Fatal("RS256 token accepted by an HS256-configured server")
	}
}

func TestJWT_HS256_StillWorks(t *testing.T) {
	secret := "a-plain-shared-secret"
	t.Setenv("CHATCLI_JWT_SECRET", secret)

	ai := NewTokenAuthInterceptor("", zap.NewNop())
	user, err := ai.validateJWT(mintHS256(t, []byte(secret), validClaims()))
	if err != nil {
		t.Fatalf("valid HS256 token rejected: %v", err)
	}
	if user.Subject != "alice" {
		t.Fatalf("unexpected subject %q", user.Subject)
	}
}

// A key path in CHATCLI_JWT_SECRET is what the documentation told operators
// to do, so it has to select RS256 rather than becoming an HMAC secret whose
// value happens to be a filename.
func TestJWT_SecretHoldingAKeyPathSelectsRS256(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("CHATCLI_JWT_SECRET", writeRSAPublicKeyPEM(t, t.TempDir(), &key.PublicKey))

	ai := NewTokenAuthInterceptor("", zap.NewNop())
	if ai.jwtAlg != jwtAlgRS256 {
		t.Fatalf("expected RS256, got %q", ai.jwtAlg)
	}
	if _, err := ai.validateJWT(mintRS256(t, key, validClaims())); err != nil {
		t.Fatalf("valid RS256 token rejected: %v", err)
	}
}

func TestJWT_RejectsAlgNone(t *testing.T) {
	t.Setenv("CHATCLI_JWT_SECRET", "a-plain-shared-secret")
	ai := NewTokenAuthInterceptor("", zap.NewNop())

	in := mintUnsigned(t, "none", validClaims())
	if _, err := ai.validateJWT(in + "."); err == nil {
		t.Fatal("alg=none token accepted")
	}
}

// --- Roles ---

func TestParseRole_DocumentedNamesGetTheDocumentedLevel(t *testing.T) {
	cases := []struct {
		claim string
		want  UserRole
		write bool
	}{
		{"admin", RoleAdmin, true},
		{"operator", RoleUser, true},
		{"user", RoleUser, true},
		{"viewer", RoleReadonly, false},
		{"readonly", RoleReadonly, false},
		{"VIEWER", RoleReadonly, false},
		{" viewer ", RoleReadonly, false},
	}
	for _, c := range cases {
		got := ParseRole(c.claim)
		if got != c.want {
			t.Errorf("ParseRole(%q) = %q, want %q", c.claim, got, c.want)
		}
		if (&UserInfo{Role: got}).HasRole(RoleUser) != c.write {
			t.Errorf("ParseRole(%q) write access = %v, want %v", c.claim, !c.write, c.write)
		}
	}
}

// The direction a typo has to fail.
func TestParseRole_UnknownRoleFailsClosed(t *testing.T) {
	for _, claim := range []string{"vewer", "superuser", "root", "Operatr"} {
		got := ParseRole(claim)
		if got != RoleReadonly {
			t.Errorf("ParseRole(%q) = %q, want %q (least privilege)", claim, got, RoleReadonly)
		}
		if _, recognized := ParseRoleStrict(claim); recognized {
			t.Errorf("ParseRoleStrict(%q) reported the role as recognized", claim)
		}
	}
}

// Tokens minted before roles existed must keep the level they were minted
// against; demoting them silently would lock working deployments out.
func TestParseRole_AbsentClaimKeepsHistoricalDefault(t *testing.T) {
	role, recognized := ParseRoleStrict("")
	if role != RoleUser || !recognized {
		t.Fatalf("empty role claim = (%q, %v), want (%q, true)", role, recognized, RoleUser)
	}
}

func TestRoleAliasesAreTheDocumentedLevels(t *testing.T) {
	if RoleViewer != RoleReadonly {
		t.Errorf("RoleViewer must alias RoleReadonly")
	}
	if RoleOperator != RoleUser {
		t.Errorf("RoleOperator must alias RoleUser")
	}
}

// --- mTLS ---

func writeCABundle(t *testing.T, dir string, count int) string {
	t.Helper()
	var out []byte
	for i := 0; i < count; i++ {
		key, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			t.Fatal(err)
		}
		tmpl := &x509.Certificate{
			SerialNumber:          big.NewInt(int64(i + 1)),
			Subject:               pkix.Name{CommonName: "test-ca"},
			NotBefore:             time.Now().Add(-time.Hour),
			NotAfter:              time.Now().Add(time.Hour),
			IsCA:                  true,
			BasicConstraintsValid: true,
			KeyUsage:              x509.KeyUsageCertSign,
		}
		der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})...)
	}
	path := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(path, out, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestMTLS_ClientCABundleRequiresAndVerifiesClientCerts(t *testing.T) {
	ca := writeCABundle(t, t.TempDir(), 2)

	cfg := &tls.Config{MinVersion: tls.VersionTLS13}
	if err := applyClientCAs(cfg, ca); err != nil {
		t.Fatalf("applyClientCAs: %v", err)
	}
	if cfg.ClientAuth != tls.RequireAndVerifyClientCert {
		t.Fatalf("ClientAuth = %v, want RequireAndVerifyClientCert", cfg.ClientAuth)
	}
	if cfg.ClientCAs == nil {
		t.Fatal("ClientCAs pool was not installed")
	}
	if n := clientCACertCount(ca); n != 2 {
		t.Errorf("clientCACertCount = %d, want 2", n)
	}
}

func TestMTLS_NoBundleLeavesTLSUnchanged(t *testing.T) {
	cfg := &tls.Config{MinVersion: tls.VersionTLS13}
	if err := applyClientCAs(cfg, ""); err != nil {
		t.Fatalf("applyClientCAs with no bundle: %v", err)
	}
	if cfg.ClientAuth != tls.NoClientCert || cfg.ClientCAs != nil {
		t.Fatal("an empty client CA must not turn plain TLS into mTLS")
	}
}

// An unreadable or malformed bundle must be an error the caller turns
// fatal, never a server that silently accepts anonymous clients.
func TestMTLS_BadBundleIsAnError(t *testing.T) {
	dir := t.TempDir()
	junk := filepath.Join(dir, "junk.pem")
	if err := os.WriteFile(junk, []byte("this is not a certificate\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &tls.Config{MinVersion: tls.VersionTLS13}
	if err := applyClientCAs(cfg, junk); err == nil {
		t.Fatal("a bundle with no PEM certificate was accepted")
	}
	if err := applyClientCAs(cfg, filepath.Join(dir, "does-not-exist.pem")); err == nil {
		t.Fatal("a missing bundle was accepted")
	}
}
