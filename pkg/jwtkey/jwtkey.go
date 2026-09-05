/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

// Package jwtkey resolves the JWT verification material a ChatCLI server
// is configured with, and reports which algorithm that material selects.
//
// It exists as a leaf package because two callers need the same answer and
// must never disagree: the server, which verifies tokens, and `/config
// security`, which tells the operator what the server will do. A second
// implementation of "does this value name an RSA key?" is a second place
// for the answer to drift.
package jwtkey

import (
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Algorithms this package can select.
const (
	AlgHS256 = "HS256"
	AlgRS256 = "RS256"
	// AlgNone reports that no JWT material is configured at all.
	AlgNone = ""
)

// ErrNoKeys reports that the material parsed but carried no RSA public key.
var ErrNoKeys = errors.New("no RSA public key found")

// Algorithm reports which algorithm the given configuration selects, from
// the raw values of CHATCLI_JWT_PUBLIC_KEY and CHATCLI_JWT_SECRET.
//
// The rule is deliberately total: an explicit public key always means
// RS256; otherwise a secret that resolves to key material means RS256 and
// anything else means HS256; no configuration at all means AlgNone. The
// server and the config screen both call this, so what the operator reads
// is what the verifier does.
func Algorithm(publicKey, secret string) string {
	if strings.TrimSpace(publicKey) != "" {
		return AlgRS256
	}
	if secret == "" {
		return AlgNone
	}
	if LooksLikeKeyMaterial(secret) {
		return AlgRS256
	}
	return AlgHS256
}

// LooksLikeKeyMaterial reports whether a value is meant as an RSA key
// rather than an HMAC secret: inline PEM, or a path to a readable file
// whose first PEM block carries a public key or a certificate.
//
// Guessing is confined to this one function on purpose. A wrong guess in
// either direction is a misconfiguration the operator must be able to see,
// which is why Algorithm is surfaced in `/config security` rather than
// left implicit.
func LooksLikeKeyMaterial(value string) bool {
	trimmed := strings.TrimSpace(value)
	if strings.HasPrefix(trimmed, "-----BEGIN ") {
		return true
	}
	if trimmed == "" || strings.ContainsAny(trimmed, "\n\r") {
		return false
	}
	info, err := os.Stat(cleanConfiguredPath(trimmed))
	if err != nil || info.IsDir() {
		return false
	}
	data, err := readConfiguredFile(trimmed)
	if err != nil {
		return false
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return false
	}
	switch block.Type {
	case "PUBLIC KEY", "RSA PUBLIC KEY", "CERTIFICATE":
		return true
	}
	return false
}

// LoadRSAPublicKeys reads one or more RSA public keys from a value that is
// either a filesystem path or inline PEM.
//
// Several keys in one file are accepted and all are returned, so a key
// rotation can run with the outgoing and the incoming key trusted at the
// same time instead of requiring a flip-the-switch cutover.
//
// Three PEM shapes are accepted, because issuers hand out all three: PKIX
// ("PUBLIC KEY"), PKCS#1 ("RSA PUBLIC KEY"), and an X.509 certificate
// ("CERTIFICATE"), from which the public key is taken.
func LoadRSAPublicKeys(value string) ([]*rsa.PublicKey, error) {
	data, err := readMaterial(value)
	if err != nil {
		return nil, err
	}

	var keys []*rsa.PublicKey
	rest := data
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		key, err := rsaKeyFromPEMBlock(block)
		if err != nil {
			return nil, err
		}
		if key != nil {
			keys = append(keys, key)
		}
	}

	if len(keys) == 0 {
		return nil, ErrNoKeys
	}
	return keys, nil
}

// readMaterial resolves the configured value to PEM bytes: inline PEM is
// used as-is, anything else is read from disk as a path.
func readMaterial(value string) ([]byte, error) {
	trimmed := strings.TrimSpace(value)
	if strings.HasPrefix(trimmed, "-----BEGIN ") {
		return []byte(trimmed), nil
	}
	data, err := readConfiguredFile(trimmed)
	if err != nil {
		return nil, fmt.Errorf("reading RSA public key: %w", err)
	}
	return data, nil
}

// cleanConfiguredPath and readConfiguredFile are the single audited point
// where this package touches the filesystem.
//
// The path is an operator-supplied deployment setting — the same class of
// input as a TLS certificate path — not caller-controlled data, and naming
// any file the operator chooses is the whole purpose. Confining the reads
// here keeps that judgement in one reviewable place instead of repeating it
// at every call site.
func cleanConfiguredPath(path string) string {
	return filepath.Clean(path)
}

func readConfiguredFile(path string) ([]byte, error) {
	return os.ReadFile(cleanConfiguredPath(path)) // #nosec G304 G703 -- operator-configured key path (deployment setting, not caller input); see cleanConfiguredPath
}

// rsaKeyFromPEMBlock extracts an RSA public key from one PEM block, or
// (nil, nil) for a block type that carries no key.
func rsaKeyFromPEMBlock(block *pem.Block) (*rsa.PublicKey, error) {
	switch block.Type {
	case "PUBLIC KEY":
		parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parsing PKIX public key: %w", err)
		}
		key, ok := parsed.(*rsa.PublicKey)
		if !ok {
			return nil, fmt.Errorf("public key is %T, not RSA — RS256 requires an RSA key", parsed)
		}
		return key, nil
	case "RSA PUBLIC KEY":
		key, err := x509.ParsePKCS1PublicKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parsing PKCS#1 public key: %w", err)
		}
		return key, nil
	case "CERTIFICATE":
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parsing certificate: %w", err)
		}
		key, ok := cert.PublicKey.(*rsa.PublicKey)
		if !ok {
			return nil, fmt.Errorf("certificate carries a %T key, not RSA — RS256 requires an RSA key", cert.PublicKey)
		}
		return key, nil
	}
	return nil, nil
}
