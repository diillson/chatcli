/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package auth

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"io"

	"golang.org/x/crypto/hkdf"
)

// dataKeyInfo separates the data key from the credential key it is derived
// from: knowing one never yields the other's use.
const dataKeyInfo = "chatcli-atrest-data-key-v1"

// DataKeySecret returns the secret that seals the local stores (sessions,
// transcripts, memory, contexts…) when CHATCLI_ENCRYPTION_KEY is not set,
// and where its root key is kept: "keychain" or "file".
//
// It is derived (HKDF-SHA256) from the credential-encryption key, so a user
// has a single key to keep — in the OS keychain wherever one is available —
// and the stores and the credentials are still sealed under different keys.
// The secret is hex, 64 characters: long enough to skip passphrase
// stretching.
func DataKeySecret() (secret, where string, err error) {
	keyPath := getKeyPath()
	root, err := loadOrCreateKey()
	if err != nil {
		return "", "", err
	}
	where = "file"
	if keyIsInKeychain(keyPath) {
		where = "keychain"
	}
	secret, err = deriveDataSecret(root)
	return secret, where, err
}

// DataKeyFallbacks returns the data secrets derived from every other
// credential key this machine still holds — the key file and the keychain
// entry when they differ from the active key. Stores sealed while one of
// them was active keep opening (read only; writes use the active key), so
// a key that changed under a store never strands it.
func DataKeyFallbacks() []string {
	active, err := loadOrCreateKey()
	if err != nil {
		active = nil
	}
	var candidates [][]byte
	if k, ferr := readKeyFileStrict(getKeyPath()); ferr == nil {
		candidates = append(candidates, k)
	}
	if k, kerr := lookupKeychainKey(NewKeychainStore()); kerr == nil {
		candidates = append(candidates, k)
	}
	var out []string
	seen := map[string]bool{}
	for _, k := range candidates {
		if active != nil && subtle.ConstantTimeCompare(k, active) == 1 {
			continue
		}
		s, derr := deriveDataSecret(k)
		if derr != nil || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// deriveDataSecret is the HKDF step from a credential key to a data secret.
func deriveDataSecret(root []byte) (string, error) {
	out := make([]byte, 32)
	if _, err := io.ReadFull(hkdf.New(sha256.New, root, nil, []byte(dataKeyInfo)), out); err != nil {
		return "", err
	}
	return hex.EncodeToString(out), nil
}

// KeyFilePath is where the credential key file lives when no OS keychain
// holds it (CHATCLI_AUTH_DIR, or ~/.chatcli).
func KeyFilePath() string {
	return getKeyPath()
}
