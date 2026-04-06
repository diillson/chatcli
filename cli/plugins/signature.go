/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package plugins

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var (
	// ErrNoSignature indicates the plugin has no .sig file.
	ErrNoSignature = errors.New("plugin signature file not found")
	// ErrInvalidSignature indicates the signature verification failed.
	ErrInvalidSignature = errors.New("plugin signature verification failed")
	// ErrNoTrustedKeys indicates no trusted public keys were found.
	ErrNoTrustedKeys = errors.New("no trusted public keys found")
)

// PluginVerifier verifies Ed25519 signatures on plugin binaries.
type PluginVerifier struct {
	trustedKeys []ed25519.PublicKey
	allowUnsigned bool
}

// NewPluginVerifier creates a verifier that loads trusted keys from ~/.chatcli/trusted-keys/.
// Set CHATCLI_ALLOW_UNSIGNED_PLUGINS=true to allow unsigned plugins (dev only).
func NewPluginVerifier() *PluginVerifier {
	v := &PluginVerifier{
		allowUnsigned: strings.EqualFold(os.Getenv("CHATCLI_ALLOW_UNSIGNED_PLUGINS"), "true"),
	}

	// Load trusted public keys
	home, err := os.UserHomeDir()
	if err != nil {
		return v
	}

	keysDir := filepath.Join(home, ".chatcli", "trusted-keys")
	entries, err := os.ReadDir(keysDir)
	if err != nil {
		return v // no keys directory is fine
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".pub") {
			continue
		}
		keyPath := filepath.Join(keysDir, entry.Name())
		key, err := loadEd25519PublicKey(keyPath)
		if err != nil {
			continue
		}
		v.trustedKeys = append(v.trustedKeys, key)
	}

	return v
}

// VerifyPlugin checks the Ed25519 signature of a plugin binary.
// The .sig file must be adjacent to the plugin (e.g., myplugin.sig for myplugin).
// .sig format: first line is base64-encoded Ed25519 signature of the plugin's SHA256 hash.
func (v *PluginVerifier) VerifyPlugin(pluginPath string) error {
	sigPath := pluginPath + ".sig"

	// Read signature file
	sigData, err := os.ReadFile(sigPath)
	if err != nil {
		if os.IsNotExist(err) {
			if v.allowUnsigned {
				return nil // development mode
			}
			return ErrNoSignature
		}
		return fmt.Errorf("failed to read signature file: %w", err)
	}

	// Parse signature (first line is base64-encoded signature)
	lines := strings.SplitN(strings.TrimSpace(string(sigData)), "\n", 2)
	if len(lines) == 0 {
		return fmt.Errorf("signature file is empty")
	}

	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(lines[0]))
	if err != nil {
		return fmt.Errorf("invalid signature encoding: %w", err)
	}

	if len(sig) != ed25519.SignatureSize {
		return fmt.Errorf("invalid signature size: expected %d, got %d", ed25519.SignatureSize, len(sig))
	}

	// Compute SHA256 hash of the plugin binary
	pluginData, err := os.ReadFile(pluginPath)
	if err != nil {
		return fmt.Errorf("failed to read plugin binary: %w", err)
	}

	hash := sha256.Sum256(pluginData)

	// Verify against trusted keys
	if len(v.trustedKeys) == 0 {
		if v.allowUnsigned {
			return nil
		}
		return ErrNoTrustedKeys
	}

	for _, key := range v.trustedKeys {
		if ed25519.Verify(key, hash[:], sig) {
			return nil
		}
	}

	return ErrInvalidSignature
}

// AllowsUnsigned returns whether unsigned plugins are permitted.
func (v *PluginVerifier) AllowsUnsigned() bool {
	return v.allowUnsigned
}

// HasTrustedKeys returns whether any trusted public keys are loaded.
func (v *PluginVerifier) HasTrustedKeys() bool {
	return len(v.trustedKeys) > 0
}

// loadEd25519PublicKey loads an Ed25519 public key from a PEM file.
func loadEd25519PublicKey(path string) (ed25519.PublicKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	block, _ := pem.Decode(data)
	if block == nil {
		// Try raw base64 (32 bytes = Ed25519 public key)
		decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(data)))
		if err != nil {
			return nil, fmt.Errorf("failed to decode key file %s", path)
		}
		if len(decoded) == ed25519.PublicKeySize {
			return ed25519.PublicKey(decoded), nil
		}
		return nil, fmt.Errorf("invalid key size in %s: expected %d bytes, got %d", path, ed25519.PublicKeySize, len(decoded))
	}

	if block.Type != "PUBLIC KEY" && block.Type != "ED25519 PUBLIC KEY" {
		return nil, fmt.Errorf("unexpected PEM type %q in %s", block.Type, path)
	}

	// Ed25519 public key is 32 bytes; in PKIX format it has an ASN.1 wrapper
	// For simplicity, accept both raw 32-byte and PKIX-wrapped keys
	if len(block.Bytes) == ed25519.PublicKeySize {
		return ed25519.PublicKey(block.Bytes), nil
	}

	// Try PKIX format (ASN.1 wrapper adds 12 bytes prefix)
	if len(block.Bytes) > ed25519.PublicKeySize {
		// The last 32 bytes of PKIX-encoded Ed25519 key are the raw key
		rawKey := block.Bytes[len(block.Bytes)-ed25519.PublicKeySize:]
		return ed25519.PublicKey(rawKey), nil
	}

	return nil, fmt.Errorf("invalid key data in %s", path)
}
