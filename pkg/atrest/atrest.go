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
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/crypto/hkdf"
)

const (
	// Magic is the header every encrypted payload starts with.
	Magic = "CHATCLI_ENC_v1\n"
	// EnvKey is the environment variable that opts a process into
	// encryption at rest and supplies the master secret.
	EnvKey = "CHATCLI_ENCRYPTION_KEY"
	// EnvPreviousKeys lists retired secrets (comma-separated) that Open may
	// still use to read payloads sealed before a rotation. Seal never uses
	// them; re-sealing every store with the current key (Reseal) retires
	// them for good.
	EnvPreviousKeys = "CHATCLI_ENCRYPTION_KEY_PREVIOUS"
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
// key was configured keeps loading (and is sealed on its next save). After a
// rotation, payloads sealed with a retired key listed in EnvPreviousKeys
// still open; they are sealed with the current key on their next save.
func Open(data []byte) ([]byte, error) {
	if !IsEncrypted(data) {
		return data, nil
	}
	enc, err := fromEnv()
	if err != nil {
		return nil, err
	}
	plain, err := enc.Decrypt(data)
	if err == nil {
		return plain, nil
	}
	for _, secret := range previousSecrets() {
		master := sha256.Sum256([]byte(secret))
		old, derr := NewFromMaster(master[:], SessionInfo)
		if derr != nil {
			continue
		}
		if plain, perr := old.Decrypt(data); perr == nil {
			return plain, nil
		}
	}
	return nil, err
}

// previousSecrets parses EnvPreviousKeys (comma-separated, blanks skipped).
func previousSecrets() []string {
	raw := os.Getenv(EnvPreviousKeys)
	if raw == "" {
		return nil
	}
	var out []string
	for _, s := range strings.Split(raw, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// SealedWithCurrentKey reports whether data opens with the current key
// alone (false for plaintext and for payloads only a retired key opens).
func SealedWithCurrentKey(data []byte) bool {
	if !IsEncrypted(data) {
		return false
	}
	enc, err := fromEnv()
	if err != nil {
		return false
	}
	_, err = enc.Decrypt(data)
	return err == nil
}

// KeyFingerprint identifies the current key for display (first 8 hex of
// SHA-256 of the derived key), "" when encryption is off.
func KeyFingerprint() string {
	enc, err := fromEnv()
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(enc.key)
	return fmt.Sprintf("%x", sum[:4])
}

// ResealFile rewrites path sealed with the current key when it is plaintext
// or sealed with a retired key. Returns whether the file was rewritten. A
// file that cannot be opened with any known key is left untouched and
// reported as an error.
func ResealFile(path string) (bool, error) {
	if !Enabled() {
		return false, ErrKeyMissing
	}
	data, err := os.ReadFile(path) // #nosec G304 G703 -- store path enumerated by the caller's walk of the operator-owned state root, never model or network input
	if err != nil {
		return false, err
	}
	if len(data) == 0 || SealedWithCurrentKey(data) {
		return false, nil
	}
	plain, err := Open(data)
	if err != nil {
		return false, err
	}
	sealed, err := Seal(plain)
	if err != nil {
		return false, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	tmp := path + ".reseal.tmp"
	if err := os.WriteFile(tmp, sealed, info.Mode().Perm()); err != nil {
		return false, err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return false, err
	}
	return true, nil
}

// ResealLines rewrites a line-oriented store (one sealed record per line,
// prefixed by linePrefix and base64-encoded) with the current key. Lines
// that are plaintext or already current are copied as they are.
func ResealLines(path, linePrefix string) (int, error) {
	if !Enabled() {
		return 0, ErrKeyMissing
	}
	data, err := os.ReadFile(path) // #nosec G304 G703 -- store path enumerated by the caller's walk of the operator-owned state root, never model or network input
	if err != nil {
		return 0, err
	}
	lines := strings.Split(string(data), "\n")
	changed := 0
	for i, line := range lines {
		if !strings.HasPrefix(line, linePrefix) {
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(line[len(linePrefix):])
		if err != nil || SealedWithCurrentKey(raw) {
			continue
		}
		plain, err := Open(raw)
		if err != nil {
			return changed, fmt.Errorf("line %d: %w", i+1, err)
		}
		sealed, err := Seal(plain)
		if err != nil {
			return changed, err
		}
		lines[i] = linePrefix + base64.StdEncoding.EncodeToString(sealed)
		changed++
	}
	if changed == 0 {
		return 0, nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	tmp := path + ".reseal.tmp"
	if err := os.WriteFile(tmp, []byte(strings.Join(lines, "\n")), info.Mode().Perm()); err != nil {
		return 0, err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return 0, err
	}
	return changed, nil
}
