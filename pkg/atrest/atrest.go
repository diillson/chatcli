/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

// Package atrest is the single encryption-at-rest primitive for ChatCLI's
// durable stores (saved sessions, autosaves, MCP session mirrors, park
// snapshots). It is a leaf package on purpose: stores that cannot import cli
// (cli/agent/park) and cli itself share one format and one key derivation.
//
// Format: Magic + 12-byte GCM nonce + AES-256-GCM ciphertext (tag included).
// Key: HKDF-SHA256(master = SHA-256(CHATCLI_ENCRYPTION_KEY), info) → 32 bytes,
// the exact derivation the original cli.SessionEncryptor documented, so a file
// written by either path opens with the other.
//
// Policy: encryption is an explicit opt-in — Seal encrypts only while
// CHATCLI_ENCRYPTION_KEY is set, and Open transparently passes plaintext
// through, so an existing plaintext store keeps loading and is re-written
// encrypted on its next save (transparent migration). An encrypted file with
// no key present is an error, never silently empty.
package atrest

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"

	"golang.org/x/crypto/hkdf"
)

const (
	// Magic is the header every encrypted payload starts with.
	Magic = "CHATCLI_ENC_v1\n"
	// EnvKey is the environment variable that opts a process into
	// encryption at rest and supplies the master secret.
	EnvKey = "CHATCLI_ENCRYPTION_KEY"
	// SessionInfo is the HKDF info string for session-class stores.
	SessionInfo = "chatcli-session-encryption"
)

// ErrKeyMissing is returned by Open when the payload is encrypted but no key
// is configured in the environment.
var ErrKeyMissing = errors.New("encrypted at rest but " + EnvKey + " is not set")

// Encryptor seals and opens payloads with one derived AES-256 key.
type Encryptor struct {
	key []byte
}

// NewFromMaster derives an Encryptor from master key material via
// HKDF-SHA256 with the given info string.
func NewFromMaster(master []byte, info string) (*Encryptor, error) {
	if len(master) == 0 {
		return nil, errors.New("atrest: empty master key")
	}
	derived := make([]byte, 32)
	if _, err := io.ReadFull(hkdf.New(sha256.New, master, nil, []byte(info)), derived); err != nil {
		return nil, fmt.Errorf("atrest: key derivation failed: %w", err)
	}
	return &Encryptor{key: derived}, nil
}

// Encrypt returns Magic + nonce + ciphertext for plaintext.
func (e *Encryptor) Encrypt(plaintext []byte) ([]byte, error) {
	gcm, err := e.gcm()
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("atrest: nonce generation failed: %w", err)
	}
	out := make([]byte, 0, len(Magic)+len(nonce)+len(plaintext)+gcm.Overhead())
	out = append(out, Magic...)
	out = append(out, nonce...)
	return gcm.Seal(out, nonce, plaintext, nil), nil
}

// Decrypt validates the header and returns the plaintext. A wrong key or a
// tampered payload fails authentication.
func (e *Encryptor) Decrypt(data []byte) ([]byte, error) {
	if !IsEncrypted(data) {
		return nil, errors.New("atrest: not an encrypted payload (missing magic header)")
	}
	gcm, err := e.gcm()
	if err != nil {
		return nil, err
	}
	body := data[len(Magic):]
	if len(body) < gcm.NonceSize() {
		return nil, errors.New("atrest: payload too short for nonce")
	}
	nonce, ciphertext := body[:gcm.NonceSize()], body[gcm.NonceSize():]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("atrest: decryption failed (tampered or wrong key): %w", err)
	}
	return plaintext, nil
}

func (e *Encryptor) gcm() (cipher.AEAD, error) {
	block, err := aes.NewCipher(e.key)
	if err != nil {
		return nil, fmt.Errorf("atrest: cipher init failed: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("atrest: GCM init failed: %w", err)
	}
	return gcm, nil
}

// IsEncrypted reports whether data carries the encrypted-payload header.
func IsEncrypted(data []byte) bool {
	return len(data) >= len(Magic) && string(data[:len(Magic)]) == Magic
}

// Enabled reports whether the process opted into encryption at rest.
func Enabled() bool {
	return os.Getenv(EnvKey) != ""
}

// fromEnv builds the session-class Encryptor from CHATCLI_ENCRYPTION_KEY.
// Re-derived per call: the derivation costs microseconds and reading the
// environment each time keeps behavior consistent with a runtime reload.
func fromEnv() (*Encryptor, error) {
	secret := os.Getenv(EnvKey)
	if secret == "" {
		return nil, ErrKeyMissing
	}
	master := sha256.Sum256([]byte(secret))
	return NewFromMaster(master[:], SessionInfo)
}

// Seal encrypts plaintext when encryption at rest is enabled and returns it
// unchanged otherwise. Stores call it right before writing.
func Seal(plaintext []byte) ([]byte, error) {
	if !Enabled() {
		return plaintext, nil
	}
	enc, err := fromEnv()
	if err != nil {
		return nil, err
	}
	return enc.Encrypt(plaintext)
}

// Open decrypts an encrypted payload and passes plaintext through untouched.
// Stores call it right after reading, so a plaintext file written before the
// key was configured keeps loading (and is sealed on its next save).
func Open(data []byte) ([]byte, error) {
	if !IsEncrypted(data) {
		return data, nil
	}
	enc, err := fromEnv()
	if err != nil {
		return nil, err
	}
	return enc.Decrypt(data)
}
