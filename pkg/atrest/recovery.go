/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package atrest

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"

	"golang.org/x/crypto/argon2"
)

// A recovery file holds the recovery key (RecoveryKey) sealed with a
// passphrase the user chooses, so it can be kept anywhere — a USB stick, a
// cloud drive — without exposing the key: the file alone opens nothing.
//
// The passphrase is stretched with Argon2id (random salt per file, the
// parameters stored in the file so they can be raised later) into an
// AES-256-GCM key; the format name and the parameters are authenticated
// data, so tampering with either fails the open.

const (
	// RecoveryFileFormat names version 1 of the recovery file.
	RecoveryFileFormat = "chatcli-recovery-v1"
	// MinRecoveryPassphrase is the shortest passphrase a recovery file
	// accepts: the file may end up anywhere, and only the passphrase and
	// Argon2id stand between it and the data.
	MinRecoveryPassphrase = 12
)

// Argon2id parameters for recovery files: deliberately heavier than the
// at-rest passphrase stretch, since a recovery file is opened once in a
// while and is the copy most likely to leave the machine.
const (
	recoveryTime    = 4
	recoveryMemory  = 128 * 1024
	recoveryThreads = 4
)

// ErrRecoveryPassphrase reports a passphrase that does not open the file
// (or a file that was modified).
var ErrRecoveryPassphrase = errors.New("wrong passphrase, or the recovery file was modified")

// recoveryFile is the on-disk JSON.
type recoveryFile struct {
	Format      string `json:"format"`
	KDF         string `json:"kdf"`
	Time        uint32 `json:"time"`
	Memory      uint32 `json:"memory_kib"`
	Threads     uint8  `json:"threads"`
	Salt        []byte `json:"salt"`
	Nonce       []byte `json:"nonce"`
	Ciphertext  []byte `json:"ciphertext"`
	Fingerprint string `json:"fingerprint,omitempty"`
}

// SealRecoveryKey seals key with passphrase into a recovery file.
// fingerprint (the key's display fingerprint) is stored in clear so the
// user can tell which installation a file belongs to.
func SealRecoveryKey(key, passphrase, fingerprint string) ([]byte, error) {
	if len(passphrase) < MinRecoveryPassphrase {
		return nil, fmt.Errorf("passphrase shorter than %d characters", MinRecoveryPassphrase)
	}
	if key == "" {
		return nil, errors.New("empty recovery key")
	}
	f := recoveryFile{
		Format: RecoveryFileFormat, KDF: "argon2id",
		Time: recoveryTime, Memory: recoveryMemory, Threads: recoveryThreads,
		Salt: make([]byte, 16), Fingerprint: fingerprint,
	}
	if _, err := rand.Read(f.Salt); err != nil {
		return nil, err
	}
	gcm, err := recoveryAEAD(passphrase, f)
	if err != nil {
		return nil, err
	}
	f.Nonce = make([]byte, gcm.NonceSize())
	if _, err := rand.Read(f.Nonce); err != nil {
		return nil, err
	}
	f.Ciphertext = gcm.Seal(nil, f.Nonce, []byte(key), recoveryAAD(f))
	return json.MarshalIndent(f, "", "  ")
}

// OpenRecoveryKey opens a recovery file with passphrase and returns the
// recovery key.
func OpenRecoveryKey(data []byte, passphrase string) (string, error) {
	var f recoveryFile
	if err := json.Unmarshal(data, &f); err != nil {
		return "", fmt.Errorf("not a ChatCLI recovery file: %w", err)
	}
	if f.Format != RecoveryFileFormat || f.KDF != "argon2id" {
		return "", fmt.Errorf("unsupported recovery file format %q", f.Format)
	}
	// Bounds before the KDF runs: a crafted file must not make Argon2id
	// allocate gigabytes or spin for minutes.
	if f.Time == 0 || f.Time > 16 || f.Memory < 8*1024 || f.Memory > 1024*1024 || f.Threads == 0 || len(f.Salt) < 16 {
		return "", errors.New("recovery file parameters out of range")
	}
	gcm, err := recoveryAEAD(passphrase, f)
	if err != nil {
		return "", err
	}
	if len(f.Nonce) != gcm.NonceSize() {
		return "", errors.New("recovery file nonce has the wrong size")
	}
	plain, err := gcm.Open(nil, f.Nonce, f.Ciphertext, recoveryAAD(f))
	if err != nil {
		return "", ErrRecoveryPassphrase
	}
	return string(plain), nil
}

// recoveryAEAD derives the file key from the passphrase.
func recoveryAEAD(passphrase string, f recoveryFile) (cipher.AEAD, error) {
	key := argon2.IDKey([]byte(passphrase), f.Salt, f.Time, f.Memory, f.Threads, 32)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// recoveryAAD binds the format, the KDF parameters, the salt and the
// fingerprint: changing any of them makes the file fail to open.
func recoveryAAD(f recoveryFile) []byte {
	return []byte(fmt.Sprintf("%s|%s|%d|%d|%d|%x|%s", f.Format, f.KDF, f.Time, f.Memory, f.Threads, f.Salt, f.Fingerprint))
}
