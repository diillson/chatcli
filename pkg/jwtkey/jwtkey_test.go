/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package jwtkey

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func rsaKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func writePEM(t *testing.T, dir, name, blockType string, der []byte) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// Issuers hand out all three shapes; refusing two of them would send
// operators looking for a converter.
func TestLoadRSAPublicKeys_AcceptsPKIXPKCS1AndCertificate(t *testing.T) {
	dir := t.TempDir()
	key := rsaKey(t)

	pkix1, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	paths := []string{
		writePEM(t, dir, "pkix.pem", "PUBLIC KEY", pkix1),
		writePEM(t, dir, "pkcs1.pem", "RSA PUBLIC KEY", x509.MarshalPKCS1PublicKey(&key.PublicKey)),
	}

	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "issuer"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	paths = append(paths, writePEM(t, dir, "cert.pem", "CERTIFICATE", der))

	for _, p := range paths {
		keys, err := LoadRSAPublicKeys(p)
		if err != nil {
			t.Errorf("%s: %v", filepath.Base(p), err)
			continue
		}
		if len(keys) != 1 || !keys[0].Equal(&key.PublicKey) {
			t.Errorf("%s: did not round-trip the key", filepath.Base(p))
		}
	}
}

// A rotation needs both keys trusted at once, or it needs a cutover.
func TestLoadRSAPublicKeys_ReturnsEveryKeyInABundle(t *testing.T) {
	dir := t.TempDir()
	a, b := rsaKey(t), rsaKey(t)

	derA, err := x509.MarshalPKIXPublicKey(&a.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	derB, err := x509.MarshalPKIXPublicKey(&b.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	bundle := append(
		pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: derA}),
		pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: derB})...,
	)
	path := filepath.Join(dir, "bundle.pem")
	if err := os.WriteFile(path, bundle, 0o600); err != nil {
		t.Fatal(err)
	}

	keys, err := LoadRSAPublicKeys(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 {
		t.Fatalf("loaded %d keys from a two-key bundle", len(keys))
	}
}

func TestLoadRSAPublicKeys_InlinePEM(t *testing.T) {
	key := rsaKey(t)
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	inline := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))

	keys, err := LoadRSAPublicKeys(inline)
	if err != nil {
		t.Fatalf("inline PEM rejected: %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("loaded %d keys", len(keys))
	}
}

func TestLoadRSAPublicKeys_Failures(t *testing.T) {
	dir := t.TempDir()

	if _, err := LoadRSAPublicKeys(filepath.Join(dir, "missing.pem")); err == nil {
		t.Error("missing file accepted")
	}

	junk := filepath.Join(dir, "junk.pem")
	if err := os.WriteFile(junk, []byte("not pem\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadRSAPublicKeys(junk); err == nil {
		t.Error("non-PEM file accepted")
	}

	// An EC key is a valid public key and still cannot verify RS256; the
	// error has to say so rather than fail later at verification time.
	ec, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ecDER, err := x509.MarshalPKIXPublicKey(&ec.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	ecPath := writePEM(t, dir, "ec.pem", "PUBLIC KEY", ecDER)
	if _, err := LoadRSAPublicKeys(ecPath); err == nil {
		t.Error("an EC key was accepted for RS256")
	}

	// A PEM file with only irrelevant blocks parses but carries no key.
	empty := writePEM(t, dir, "params.pem", "DH PARAMETERS", []byte{0x01, 0x02})
	if _, err := LoadRSAPublicKeys(empty); err == nil {
		t.Error("a PEM file with no key was accepted")
	}
}

func TestAlgorithm_SelectsOneAlgorithm(t *testing.T) {
	dir := t.TempDir()
	key := rsaKey(t)
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	pubPath := writePEM(t, dir, "pub.pem", "PUBLIC KEY", der)

	cases := []struct {
		name      string
		publicKey string
		secret    string
		want      string
	}{
		{"nothing configured", "", "", AlgNone},
		{"plain secret", "", "hunter2-hunter2-hunter2", AlgHS256},
		{"explicit public key", pubPath, "", AlgRS256},
		{"explicit key wins over secret", pubPath, "hunter2", AlgRS256},
		{"secret holding a key path", "", pubPath, AlgRS256},
		{"secret that merely looks like a path", "", filepath.Join(dir, "nope.pem"), AlgHS256},
	}
	for _, c := range cases {
		if got := Algorithm(c.publicKey, c.secret); got != c.want {
			t.Errorf("%s: Algorithm = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestLooksLikeKeyMaterial(t *testing.T) {
	dir := t.TempDir()
	key := rsaKey(t)
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	pubPath := writePEM(t, dir, "pub.pem", "PUBLIC KEY", der)

	if !LooksLikeKeyMaterial(pubPath) {
		t.Error("a key file was not recognized")
	}
	if !LooksLikeKeyMaterial("-----BEGIN PUBLIC KEY-----\nabc\n-----END PUBLIC KEY-----") {
		t.Error("inline PEM was not recognized")
	}
	for _, v := range []string{"", "  ", "a-plain-secret", "multi\nline\nsecret", dir} {
		if LooksLikeKeyMaterial(v) {
			t.Errorf("%q was mistaken for key material", v)
		}
	}

	// A PEM file that is not a public key must not select RS256.
	privDER := x509.MarshalPKCS1PrivateKey(key)
	privPath := writePEM(t, dir, "priv.pem", "RSA PRIVATE KEY", privDER)
	if LooksLikeKeyMaterial(privPath) {
		t.Error("a private key file selected RS256")
	}
}
