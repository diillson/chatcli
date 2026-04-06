/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/crypto/hkdf"
)

const (
	sessionEncMagic = "CHATCLI_ENC_v1\n" // 15 bytes magic header for encrypted sessions
)

// SessionEncryptor provides AES-256-GCM encryption for session files.
// Key is derived from the existing auth key (~/.chatcli/.auth-key) using HKDF.
type SessionEncryptor struct {
	key []byte // 32-byte AES-256 key
}

// NewSessionEncryptor creates an encryptor by deriving a key from the auth master key.
// If CHATCLI_ENCRYPTION_KEY env is set, uses that instead.
// Falls back to the auth key file at ~/.chatcli/.auth-key.
func NewSessionEncryptor() (*SessionEncryptor, error) {
	masterKey, err := loadMasterKey()
	if err != nil {
		return nil, fmt.Errorf("session encryption unavailable: %w", err)
	}

	// Derive session-specific key using HKDF-SHA256
	hkdfReader := hkdf.New(sha256.New, masterKey, nil, []byte("chatcli-session-encryption"))
	derivedKey := make([]byte, 32) // AES-256
	if _, err := io.ReadFull(hkdfReader, derivedKey); err != nil {
		return nil, fmt.Errorf("key derivation failed: %w", err)
	}

	return &SessionEncryptor{key: derivedKey}, nil
}

// Encrypt encrypts plaintext data and prepends the magic header + nonce.
// Format: CHATCLI_ENC_v1\n + 12-byte nonce + ciphertext (with GCM tag)
func (se *SessionEncryptor) Encrypt(plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(se.key)
	if err != nil {
		return nil, fmt.Errorf("cipher init failed: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("GCM init failed: %w", err)
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("nonce generation failed: %w", err)
	}

	ciphertext := gcm.Seal(nil, nonce, plaintext, nil)

	// Prepend magic header + nonce
	result := make([]byte, 0, len(sessionEncMagic)+len(nonce)+len(ciphertext))
	result = append(result, []byte(sessionEncMagic)...)
	result = append(result, nonce...)
	result = append(result, ciphertext...)

	return result, nil
}

// Decrypt decrypts data that was encrypted with Encrypt.
// Returns the plaintext. Validates the magic header.
func (se *SessionEncryptor) Decrypt(data []byte) ([]byte, error) {
	magicLen := len(sessionEncMagic)
	if len(data) < magicLen {
		return nil, fmt.Errorf("data too short for encrypted session")
	}

	if string(data[:magicLen]) != sessionEncMagic {
		return nil, fmt.Errorf("not an encrypted session (wrong magic header)")
	}

	block, err := aes.NewCipher(se.key)
	if err != nil {
		return nil, fmt.Errorf("cipher init failed: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("GCM init failed: %w", err)
	}

	nonceSize := gcm.NonceSize()
	if len(data) < magicLen+nonceSize {
		return nil, fmt.Errorf("data too short for nonce")
	}

	nonce := data[magicLen : magicLen+nonceSize]
	ciphertext := data[magicLen+nonceSize:]

	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("decryption failed (tampered or wrong key): %w", err)
	}

	return plaintext, nil
}

// IsEncrypted checks if data starts with the encrypted session magic header.
func IsEncrypted(data []byte) bool {
	return len(data) >= len(sessionEncMagic) && string(data[:len(sessionEncMagic)]) == sessionEncMagic
}

// loadMasterKey loads the encryption master key from env or auth key file.
func loadMasterKey() ([]byte, error) {
	// Prefer explicit env var
	if envKey := os.Getenv("CHATCLI_ENCRYPTION_KEY"); envKey != "" {
		// Hash to ensure consistent 32-byte key
		h := sha256.Sum256([]byte(envKey))
		return h[:], nil
	}

	// Fall back to auth key file
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("cannot determine home directory: %w", err)
	}

	keyPath := filepath.Join(homeDir, ".chatcli", ".auth-key")
	key, err := os.ReadFile(keyPath)
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
