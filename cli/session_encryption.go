/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"

	"github.com/diillson/chatcli/pkg/atrest"
)

// SessionEncryptor provides AES-256-GCM encryption for session files.
// Key is derived from the existing auth key (~/.chatcli/.auth-key) using HKDF.
//
// The format and derivation live in pkg/atrest so the session store, the MCP
// session mirrors and park snapshots (which cannot import cli) share them;
// this type remains as the cli-side handle over the same primitive.
type SessionEncryptor struct {
	inner *atrest.Encryptor
}

// NewSessionEncryptor creates an encryptor by deriving a key from the auth master key.
// If CHATCLI_ENCRYPTION_KEY env is set, uses that instead.
// Falls back to the auth key file at ~/.chatcli/.auth-key.
func NewSessionEncryptor() (*SessionEncryptor, error) {
	masterKey, err := loadMasterKey()
	if err != nil {
		return nil, fmt.Errorf("session encryption unavailable: %w", err)
	}
	inner, err := atrest.NewFromMaster(masterKey, atrest.SessionInfo)
	if err != nil {
		return nil, err
	}
	return &SessionEncryptor{inner: inner}, nil
}

// Encrypt encrypts plaintext data and prepends the magic header + nonce.
// Format: CHATCLI_ENC_v1\n + 12-byte nonce + ciphertext (with GCM tag)
func (se *SessionEncryptor) Encrypt(plaintext []byte) ([]byte, error) {
	return se.inner.Encrypt(plaintext)
}

// Decrypt decrypts data that was encrypted with Encrypt.
// Returns the plaintext. Validates the magic header.
func (se *SessionEncryptor) Decrypt(data []byte) ([]byte, error) {
	return se.inner.Decrypt(data)
}

// IsEncrypted checks if data starts with the encrypted session magic header.
func IsEncrypted(data []byte) bool {
	return atrest.IsEncrypted(data)
}

// loadMasterKey loads the encryption master key from env or auth key file.
func loadMasterKey() ([]byte, error) {
	// Prefer explicit env var
	if envKey := os.Getenv(atrest.EnvKey); envKey != "" {
		// Hash to ensure consistent 32-byte key
		h := sha256.Sum256([]byte(envKey))
		return h[:], nil
	}

	// Fall back to auth key file
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("cannot determine home directory: %w", err)
	}

	keyPath := filepath.Clean(filepath.Join(homeDir, ".chatcli", ".auth-key"))
	key, err := os.ReadFile(keyPath) // #nosec G304 -- path is constructed from user's home dir, not user input
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("no encryption key found; set CHATCLI_ENCRYPTION_KEY or run chatcli login")
		}
		return nil, fmt.Errorf("failed to read auth key: %w", err)
	}

	if len(key) < 16 {
		return nil, fmt.Errorf("auth key too short (%d bytes)", len(key))
	}

	// Derive a consistent key from the auth key bytes
	h := sha256.Sum256(key)
	return h[:], nil
}
