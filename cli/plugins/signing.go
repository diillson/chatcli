/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package plugins

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"crypto/x509"
)

// Filenames the key pair is written under. They are fixed rather than
// configurable so that a signing directory looks the same on every machine
// and in every runbook.
const (
	SigningKeyFileName = "plugin-signing.key"
	SigningPubFileName = "plugin-signing.pub"
	// SignatureSuffix is appended to a plugin's path to locate its
	// signature, matching what the verifier looks for.
	SignatureSuffix = ".sig"
)

// GenerateSigningKeyPair creates an Ed25519 key pair and writes it to dir.
//
// The private key is written 0600 and the public key 0644: one is a secret
// the signer holds, the other is meant to be copied to every machine that
// verifies. Refusing to overwrite an existing private key is deliberate —
// regenerating over a key in use silently invalidates every signature made
// with it, and that is not a mistake worth making convenient.
func GenerateSigningKeyPair(dir string) (keyPath, pubPath string, err error) {
	if dir == "" {
		return "", "", fmt.Errorf("output directory is required")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", "", fmt.Errorf("creating %s: %w", dir, err)
	}

	keyPath = filepath.Join(dir, SigningKeyFileName)
	pubPath = filepath.Join(dir, SigningPubFileName)

	if _, err := os.Stat(keyPath); err == nil {
		return "", "", fmt.Errorf("a signing key already exists at %s; "+
			"move it aside first — regenerating would invalidate every signature made with it", keyPath)
	}

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", fmt.Errorf("generating Ed25519 key pair: %w", err)
	}

	privDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return "", "", fmt.Errorf("encoding private key: %w", err)
	}
	pubDER, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return "", "", fmt.Errorf("encoding public key: %w", err)
	}

	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privDER}), 0o600); err != nil {
		return "", "", fmt.Errorf("writing %s: %w", keyPath, err)
	}
	if err := os.WriteFile(pubPath, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER}), 0o644); err != nil { // #nosec G306 -- a public key is meant to be readable and copied to verifying machines
		return "", "", fmt.Errorf("writing %s: %w", pubPath, err)
	}

	return keyPath, pubPath, nil
}

// LoadEd25519PrivateKey reads a PKCS#8 PEM private key from disk.
func LoadEd25519PrivateKey(path string) (ed25519.PrivateKey, error) {
	data, err := os.ReadFile(filepath.Clean(path)) // #nosec G304 G703 -- operator-supplied signing key path
	if err != nil {
		return nil, fmt.Errorf("reading signing key: %w", err)
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("%s is not a PEM file", path)
	}
	if block.Type != "PRIVATE KEY" && block.Type != "ED25519 PRIVATE KEY" {
		return nil, fmt.Errorf("unexpected PEM type %q in %s", block.Type, path)
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parsing signing key: %w", err)
	}
	key, ok := parsed.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("%s holds a %T key; plugin signing requires Ed25519", path, parsed)
	}
	return key, nil
}

// PluginDigest returns the SHA-256 of a plugin binary — the value that is
// signed, and the value the verifier recomputes.
func PluginDigest(binaryPath string) ([]byte, error) {
	data, err := os.ReadFile(filepath.Clean(binaryPath)) // #nosec G304 G703 -- operator-supplied plugin path
	if err != nil {
		return nil, fmt.Errorf("reading plugin binary: %w", err)
	}
	sum := sha256.Sum256(data)
	return sum[:], nil
}

// SignPlugin signs a plugin binary and writes the detached signature.
//
// The signature file's first line is the base64 Ed25519 signature over the
// binary's SHA-256 — the exact shape VerifyPlugin expects, so a signature
// produced here verifies on any ChatCLI that trusts the matching public
// key. The remaining lines are a human-readable comment, ignored by the
// verifier, so that a signature file found in a release archive says what
// it belongs to.
func SignPlugin(binaryPath, keyPath, outPath string) (string, error) {
	key, err := LoadEd25519PrivateKey(keyPath)
	if err != nil {
		return "", err
	}
	digest, err := PluginDigest(binaryPath)
	if err != nil {
		return "", err
	}

	sig := ed25519.Sign(key, digest)

	if outPath == "" {
		outPath = binaryPath + SignatureSuffix
	}

	var b strings.Builder
	b.WriteString(base64.StdEncoding.EncodeToString(sig))
	b.WriteString("\n")
	b.WriteString("# chatcli plugin signature (Ed25519 over SHA-256)\n")
	b.WriteString("# plugin: " + filepath.Base(binaryPath) + "\n")
	b.WriteString("# sha256: " + fmt.Sprintf("%x", digest) + "\n")

	if err := os.WriteFile(outPath, []byte(b.String()), 0o644); err != nil { // #nosec G306 -- a detached signature is public and ships beside the binary
		return "", fmt.Errorf("writing %s: %w", outPath, err)
	}
	return outPath, nil
}

// TrustedKeysDir is where the verifier looks for public keys.
func TrustedKeysDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".chatcli", "trusted-keys"), nil
}

// TrustPublicKey registers a public key so plugins signed with the matching
// private key verify on this machine.
//
// Without this step a signature is unverifiable: the verifier only reads
// keys it finds in the trusted directory, and there was no supported way to
// put one there.
func TrustPublicKey(pubPath, name string) (string, error) {
	key, err := loadEd25519PublicKey(pubPath)
	if err != nil {
		return "", fmt.Errorf("%s is not a usable Ed25519 public key: %w", pubPath, err)
	}
	if len(key) != ed25519.PublicKeySize {
		return "", fmt.Errorf("%s does not hold an Ed25519 public key", pubPath)
	}

	dir, err := TrustedKeysDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("creating %s: %w", dir, err)
	}

	if name == "" {
		name = strings.TrimSuffix(filepath.Base(pubPath), filepath.Ext(pubPath))
	}
	if !safeKeyName(name) {
		return "", fmt.Errorf("key name %q may only contain letters, digits, '-', '_' and '.'", name)
	}

	dest := filepath.Join(dir, name+".pub")
	der, err := x509.MarshalPKIXPublicKey(key)
	if err != nil {
		return "", fmt.Errorf("re-encoding public key: %w", err)
	}
	if err := os.WriteFile(dest, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), 0o644); err != nil { // #nosec G306 -- trusted public keys are not secret
		return "", fmt.Errorf("writing %s: %w", dest, err)
	}
	return dest, nil
}

// safeKeyName keeps a trusted-key filename from escaping its directory.
func safeKeyName(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-', r == '_', r == '.':
		default:
			return false
		}
	}
	return true
}
