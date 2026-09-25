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
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ErrNoPrivateKey reports that the material carried no RSA private key.
var ErrNoPrivateKey = errors.New("no RSA private key found")

// ErrNoExpiry reports a token requested without an expiry. The server
// rejects such tokens, so issuing one is always a mistake.
var ErrNoExpiry = errors.New("jwt claims need an expiry")

// Claims is the set of claims the ChatCLI server understands. ExpiresAt is
// mandatory; the rest map to the server's optional checks (issuer,
// audience) and identity (subject, role, tenant, email).
type Claims struct {
	Subject   string
	Role      string
	Issuer    string
	Audience  string
	TenantID  string
	Email     string
	ExpiresAt int64 // unix seconds, required
	NotBefore int64 // unix seconds, optional
	IssuedAt  int64 // unix seconds, optional
}

// payload renders the claims as the JSON object the server parses.
func (c Claims) payload() (map[string]string, map[string]int64, error) {
	if c.ExpiresAt <= 0 {
		return nil, nil, ErrNoExpiry
	}
	strs := map[string]string{}
	set := func(k, v string) {
		if strings.TrimSpace(v) != "" {
			strs[k] = v
		}
	}
	set("sub", c.Subject)
	set("role", c.Role)
	set("iss", c.Issuer)
	set("aud", c.Audience)
	set("tenant_id", c.TenantID)
	set("email", c.Email)
	nums := map[string]int64{"exp": c.ExpiresAt}
	if c.NotBefore > 0 {
		nums["nbf"] = c.NotBefore
	}
	if c.IssuedAt > 0 {
		nums["iat"] = c.IssuedAt
	}
	return strs, nums, nil
}

// SignHS256 issues a compact JWT over claims with the shared secret the
// server verifies (CHATCLI_JWT_SECRET).
func SignHS256(claims Claims, secret []byte) (string, error) {
	if len(secret) == 0 {
		return "", errors.New("empty HS256 secret")
	}
	input, err := signingInput(AlgHS256, claims)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(input))
	return input + "." + b64url(mac.Sum(nil)), nil
}

// SignRS256 issues a compact JWT over claims with an RSA private key whose
// public half the server trusts (CHATCLI_JWT_PUBLIC_KEY).
func SignRS256(claims Claims, key *rsa.PrivateKey) (string, error) {
	if key == nil {
		return "", errors.New("nil RS256 key")
	}
	input, err := signingInput(AlgRS256, claims)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256([]byte(input))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		return "", fmt.Errorf("rs256 sign: %w", err)
	}
	return input + "." + b64url(sig), nil
}

// LoadRSAPrivateKey parses an RSA private key from PEM (PKCS#1 or PKCS#8),
// given inline or as a path to a file, mirroring LoadRSAPublicKeys.
func LoadRSAPrivateKey(value string) (*rsa.PrivateKey, error) {
	data, err := keyMaterial(value)
	if err != nil {
		return nil, err
	}
	for rest := data; len(rest) > 0; {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		switch block.Type {
		case "RSA PRIVATE KEY":
			return x509.ParsePKCS1PrivateKey(block.Bytes)
		case "PRIVATE KEY":
			k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
			if err != nil {
				return nil, err
			}
			rsaKey, ok := k.(*rsa.PrivateKey)
			if !ok {
				return nil, fmt.Errorf("PKCS#8 key is %T, not RSA", k)
			}
			return rsaKey, nil
		}
	}
	return nil, ErrNoPrivateKey
}

// keyMaterial returns the PEM bytes for value: the file it names when it is
// a path, else the value itself.
func keyMaterial(value string) ([]byte, error) {
	v := strings.TrimSpace(value)
	if v == "" {
		return nil, ErrNoPrivateKey
	}
	if strings.Contains(v, "-----BEGIN") {
		return []byte(v), nil
	}
	data, err := os.ReadFile(filepath.Clean(v)) // #nosec G304 G703 -- operator-configured key path
	if err != nil {
		return nil, fmt.Errorf("reading private key %q: %w", v, err)
	}
	return data, nil
}

// signingInput builds header.payload for the algorithm.
func signingInput(alg string, claims Claims) (string, error) {
	header, err := json.Marshal(map[string]string{"alg": alg, "typ": "JWT"})
	if err != nil {
		return "", err
	}
	strs, nums, err := claims.payload()
	if err != nil {
		return "", err
	}
	body := make(map[string]json.RawMessage, len(strs)+len(nums))
	for k, v := range strs {
		raw, err := json.Marshal(v)
		if err != nil {
			return "", fmt.Errorf("marshal claim %s: %w", k, err)
		}
		body[k] = raw
	}
	for k, v := range nums {
		body[k] = json.RawMessage(strconv.FormatInt(v, 10))
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("marshal claims: %w", err)
	}
	return b64url(header) + "." + b64url(payload), nil
}

func b64url(b []byte) string {
	return base64.RawURLEncoding.EncodeToString(b)
}
