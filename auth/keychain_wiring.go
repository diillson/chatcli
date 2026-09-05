/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package auth

import (
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// keychainAccount is the account name the credential-encryption key is
// filed under, in every backend.
const keychainAccount = "auth-key"

var keychainWarnOnce sync.Once

// keychainWarnf reports a keychain problem once per process. The condition
// that produces it — a locked keychain, a missing helper — repeats on every
// call, and a warning printed on every credential read is a warning nobody
// reads.
func keychainWarnf(format string, args ...interface{}) {
	keychainWarnOnce.Do(func() {
		fmt.Fprintf(os.Stderr, "chatcli: "+format+"\n", args...)
	})
}

// keyFromKeychain returns the stored credential-encryption key, or
// (nil, false) when the backend holds none or cannot be reached.
//
// Keys are stored base64-encoded because the platform stores are
// text-oriented: the macOS security CLI and secret-tool both round-trip
// text, and a raw 32-byte key contains bytes neither survives.
func keyFromKeychain(ks *KeychainStore) ([]byte, bool) {
	if ks == nil || !ks.IsNativeAvailable() {
		return nil, false
	}
	raw, err := ks.Get(keychainAccount)
	if err != nil || len(raw) == 0 {
		return nil, false
	}
	key, err := base64.StdEncoding.DecodeString(string(raw))
	if err != nil || len(key) != 32 {
		return nil, false
	}
	return key, true
}

// storeKeyInKeychain writes the key and verifies it reads back.
//
// The read-back is the point: a write that appears to succeed and a read
// that returns nothing would leave the process holding a key that no future
// process can find, which for an encryption key means the credentials
// become unreadable. Nothing is removed from disk until this returns true.
func storeKeyInKeychain(ks *KeychainStore, key []byte) bool {
	if ks == nil || !ks.IsNativeAvailable() {
		return false
	}
	encoded := []byte(base64.StdEncoding.EncodeToString(key))
	if err := ks.Set(keychainAccount, encoded); err != nil {
		keychainWarnf("could not store the credential key in the OS keychain (%v); keeping the file backend", err)
		return false
	}
	readBack, ok := keyFromKeychain(ks)
	if !ok || subtle.ConstantTimeCompare(readBack, key) != 1 {
		keychainWarnf("the OS keychain did not return the key just written; keeping the file backend")
		return false
	}
	return true
}

// resolveEncryptionKey decides where the credential-encryption key lives.
//
// Three backends, and the difference between them is what happens to a key
// that already exists on disk:
//
//   - file: the file, always. Nothing else is consulted.
//   - keychain: the keychain. A key already in the file is migrated into it
//     once, and the file is removed only after the keychain has been read
//     back successfully.
//   - auto (the default): an existing file key keeps being used, exactly as
//     before. Only a key being created for the first time goes to the
//     keychain, and only where one is available. Moving a working
//     installation's key without being asked is not a default's business.
func resolveEncryptionKey(ks *KeychainStore, keyPath string) ([]byte, error) {
	fileKey, hasFile := readKeyFile(keyPath)

	switch {
	case ks.backend == KeychainFile:
		// Explicit file backend: never touch the keychain.

	case ks.backend == KeychainNative:
		if key, ok := keyFromKeychain(ks); ok {
			return key, nil
		}
		if !ks.IsNativeAvailable() {
			keychainWarnf("CHATCLI_KEYCHAIN_BACKEND=keychain but no OS keychain is available here; using the file backend")
			break
		}
		if hasFile {
			// One-time migration, verified before the file goes away.
			if storeKeyInKeychain(ks, fileKey) {
				if err := os.Remove(keyPath); err != nil {
					keychainWarnf("credential key copied to the OS keychain, but %s could not be removed (%v)", keyPath, err)
				}
				return fileKey, nil
			}
			return fileKey, nil
		}

	default: // KeychainAuto
		if hasFile {
			return fileKey, nil
		}
		if key, ok := keyFromKeychain(ks); ok {
			return key, nil
		}
	}

	if hasFile {
		return fileKey, nil
	}

	key, err := newRandomKey()
	if err != nil {
		return nil, err
	}

	// A brand-new key goes to the keychain when one is usable and the
	// operator has not pinned the file backend.
	if ks.backend != KeychainFile && storeKeyInKeychain(ks, key) {
		return key, nil
	}
	if err := writeKeyFile(keyPath, key); err != nil {
		return nil, err
	}
	return key, nil
}

// readKeyFile returns the on-disk key when it is present and well-formed.
func readKeyFile(keyPath string) ([]byte, bool) {
	data, err := os.ReadFile(filepath.Clean(keyPath)) //#nosec G304 G703 -- key path derived from CHATCLI_AUTH_DIR or the home directory
	if err != nil || len(data) != 32 {
		return nil, false
	}
	return data, true
}
