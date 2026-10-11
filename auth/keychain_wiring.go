/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package auth

import (
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/diillson/chatcli/i18n"
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

// lookupKeychainKey returns the stored credential-encryption key.
//
// The error separates the two outcomes that must never be confused: an
// error wrapping os.ErrNotExist means the keychain was reached and holds no
// usable key (nothing stored, no keychain on this machine, or an entry that
// is not a 32-byte key and so can protect nothing). Any other error means
// the keychain could not answer — locked, access denied, helper failing —
// and a key may well be there.
//
// Keys are stored base64-encoded because the platform stores are
// text-oriented: the macOS security CLI and secret-tool both round-trip
// text, and a raw 32-byte key contains bytes neither survives.
func lookupKeychainKey(ks *KeychainStore) ([]byte, error) {
	if ks == nil || !ks.IsNativeAvailable() {
		return nil, errKeychainItemNotFound
	}
	raw, err := ks.Get(keychainAccount)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, errKeychainItemNotFound
		}
		return nil, err
	}
	if len(raw) == 0 {
		return nil, errKeychainItemNotFound
	}
	key, err := base64.StdEncoding.DecodeString(string(raw))
	if err != nil || len(key) != 32 {
		return nil, errKeychainItemNotFound
	}
	return key, nil
}

// keyFromKeychain returns the stored credential-encryption key, or
// (nil, false) when the backend holds none or cannot be reached.
func keyFromKeychain(ks *KeychainStore) ([]byte, bool) {
	key, err := lookupKeychainKey(ks)
	return key, err == nil
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

// publishNewKey stores a freshly generated key in the keychain and returns
// the key the keychain holds afterwards. When another process created the
// first key at the same moment and its write landed last, that key is the
// one every later process will read, so it is the one adopted here.
func publishNewKey(ks *KeychainStore, key []byte) ([]byte, bool) {
	if storeKeyInKeychain(ks, key) {
		return key, true
	}
	if held, err := lookupKeychainKey(ks); err == nil {
		return held, true
	}
	return nil, false
}

// keychainUnreadableError is the fail-closed answer when the keychain could
// not be read: no key is generated, because a new key would replace the one
// the keychain is holding and orphan every credential encrypted with it.
func keychainUnreadableError(cause error) error {
	return fmt.Errorf("%s: %w", i18n.T("auth.keychain.unreadable", keychainEnvVar), cause)
}

// keychainEnvVar is the setting that selects the key backend.
const keychainEnvVar = "CHATCLI_KEYCHAIN_BACKEND"

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
//
// A new key is generated only when every backend in play answered that it
// holds none. A keychain that cannot be read, or a key file that exists but
// cannot be read or is malformed, is an error: generating a key then would
// silently replace the real one.
func resolveEncryptionKey(ks *KeychainStore, keyPath string) ([]byte, error) {
	fileKey, err := readKeyFileStrict(keyPath)
	hasFile := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}

	switch ks.backend {
	case KeychainFile:
		// Explicit file backend: never touch the keychain.

	case KeychainNative:
		key, kerr := lookupKeychainKey(ks)
		if kerr == nil {
			return key, nil
		}
		if !ks.IsNativeAvailable() {
			keychainWarnf("CHATCLI_KEYCHAIN_BACKEND=keychain but no OS keychain is available here; using the file backend")
			break
		}
		if !errors.Is(kerr, os.ErrNotExist) {
			return nil, keychainUnreadableError(kerr)
		}
		if hasFile {
			// One-time migration, verified before the file goes away.
			if storeKeyInKeychain(ks, fileKey) {
				if err := os.Remove(keyPath); err != nil {
					keychainWarnf("credential key copied to the OS keychain, but %s could not be removed (%v)", keyPath, err)
				}
			}
			return fileKey, nil
		}

	default: // KeychainAuto
		if hasFile {
			return fileKey, nil
		}
		key, kerr := lookupKeychainKey(ks)
		if kerr == nil {
			return key, nil
		}
		if !errors.Is(kerr, os.ErrNotExist) {
			return nil, keychainUnreadableError(kerr)
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
	if ks.backend != KeychainFile && ks.IsNativeAvailable() {
		if held, ok := publishNewKey(ks, key); ok {
			return held, nil
		}
	}
	return createKeyFile(keyPath, key)
}

// readKeyFile returns the on-disk key when it is present and well-formed.
func readKeyFile(keyPath string) ([]byte, bool) {
	key, err := readKeyFileStrict(keyPath)
	return key, err == nil
}

// readKeyFileStrict returns the on-disk key. A missing file is an error
// wrapping os.ErrNotExist; a file that exists but cannot be read, or does
// not hold a 32-byte key, is a different error, since replacing it would
// destroy whatever key it was meant to hold.
func readKeyFileStrict(keyPath string) ([]byte, error) {
	data, err := os.ReadFile(filepath.Clean(keyPath)) //#nosec G304 G703 -- key path derived from CHATCLI_AUTH_DIR or the home directory
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		return nil, fmt.Errorf("%s: %w", i18n.T("auth.keyfile.invalid", keyPath), err)
	}
	if len(data) != 32 {
		return nil, fmt.Errorf("%s: %s", i18n.T("auth.keyfile.invalid", keyPath), i18n.T("auth.keyfile.bad_length", len(data)))
	}
	return data, nil
}
