/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package jwtkey

import (
	"crypto"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func decodePart(t *testing.T, s string) map[string]any {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(s)
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(raw, &m))
	return m
}

func TestSignHS256_ProducesAVerifiableCompactToken(t *testing.T) {
	tok, err := SignHS256(Claims{Subject: "chatcli-operator", Role: "operator", ExpiresAt: 4102444800, TenantID: "t1"}, []byte("s3cr3t"))
	require.NoError(t, err)
	parts := strings.Split(tok, ".")
	require.Len(t, parts, 3)
	assert.Equal(t, "HS256", decodePart(t, parts[0])["alg"])
	payload := decodePart(t, parts[1])
	assert.Equal(t, "chatcli-operator", payload["sub"])
	assert.Equal(t, "operator", payload["role"])
	assert.Equal(t, "t1", payload["tenant_id"])
	assert.Equal(t, float64(4102444800), payload["exp"])
	_, hasNbf := payload["nbf"]
	assert.False(t, hasNbf, "optional claims are omitted when zero")

	mac := hmac.New(sha256.New, []byte("s3cr3t"))
	mac.Write([]byte(parts[0] + "." + parts[1]))
	assert.Equal(t, base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), parts[2])

	_, err = SignHS256(Claims{Subject: "x", ExpiresAt: 1}, nil)
	assert.Error(t, err, "an empty secret signs nothing")
	_, err = SignHS256(Claims{Subject: "x"}, []byte("s"))
	assert.ErrorIs(t, err, ErrNoExpiry, "a token without expiry is never issued")
}

func TestSignRS256_VerifiesWithThePublicKey(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	tok, err := SignRS256(Claims{Subject: "op", ExpiresAt: 4102444800, NotBefore: 1, IssuedAt: 1}, key)
	require.NoError(t, err)
	parts := strings.Split(tok, ".")
	require.Len(t, parts, 3)
	assert.Equal(t, "RS256", decodePart(t, parts[0])["alg"])
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	require.NoError(t, err)
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	assert.NoError(t, rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, digest[:], sig))

	_, err = SignRS256(Claims{Subject: "x", ExpiresAt: 1}, nil)
	assert.Error(t, err)
}

func TestLoadRSAPrivateKey_PKCS1PKCS8InlineAndFile(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	pkcs1 := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	der, err := x509.MarshalPKCS8PrivateKey(key)
	require.NoError(t, err)
	pkcs8 := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})

	got, err := LoadRSAPrivateKey(string(pkcs1))
	require.NoError(t, err)
	assert.True(t, got.Equal(key))
	got, err = LoadRSAPrivateKey(string(pkcs8))
	require.NoError(t, err)
	assert.True(t, got.Equal(key))

	path := filepath.Join(t.TempDir(), "key.pem")
	require.NoError(t, os.WriteFile(path, pkcs1, 0o600))
	got, err = LoadRSAPrivateKey(path)
	require.NoError(t, err)
	assert.True(t, got.Equal(key))

	_, err = LoadRSAPrivateKey("")
	assert.ErrorIs(t, err, ErrNoPrivateKey)
	_, err = LoadRSAPrivateKey(filepath.Join(t.TempDir(), "missing.pem"))
	assert.Error(t, err)
	pub := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: func() []byte { b, _ := x509.MarshalPKIXPublicKey(&key.PublicKey); return b }()})
	_, err = LoadRSAPrivateKey(string(pub))
	assert.ErrorIs(t, err, ErrNoPrivateKey, "a public key is not a private key")
}
