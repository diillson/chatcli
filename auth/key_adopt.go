/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package auth

import (
	"crypto/subtle"
	"errors"
	"os"
	"sync"

	"github.com/diillson/chatcli/i18n"
)

var (
	adoptMu   sync.Mutex
	adoptDone = map[string]bool{}
)

// keychainMarkerPath records, next to the key file, that the credential
// key lives in the OS keychain. It holds nothing secret. Once it exists no
// process creates a new key for this installation: a keychain that cannot
// be reached (an SSH session, a container, a cron job with the file
// backend) or that answers "no such item" while the key should be there
// is an error, never a reason to start over with a key that would orphan
// everything sealed with the real one.
func keychainMarkerPath(keyPath string) string {
	return keyPath + ".keychain"
}

// markKeyInKeychain writes the marker; best effort (the key itself is
// already safe in the keychain).
func markKeyInKeychain(keyPath string) {
	path := keychainMarkerPath(keyPath)
	if _, err := os.Stat(path); err == nil {
		return
	}
	_ = os.WriteFile(path, []byte("the credential key of this installation is in the OS keychain\n"), 0o600) // #nosec G306 G703 -- marker next to the key file; no secret
}

// keyIsInKeychain reports whether the marker exists.
func keyIsInKeychain(keyPath string) bool {
	_, err := os.Stat(keychainMarkerPath(keyPath))
	return err == nil
}

// errKeyNotInKeychain is the fail-closed answer when the marker says the key
// is in the keychain and this process cannot get it from there.
func errKeyNotInKeychain(keyPath string) error {
	return errors.New(i18n.T("auth.keychain.key_missing", keychainMarkerPath(keyPath), keychainEnvVar))
}

// adoptFileKey moves the credential key file into the OS keychain under the
// auto backend, so the key no longer sits on disk next to the data it
// protects. It runs once per key path per process and only for the default
// location (CHATCLI_AUTH_DIR unset): a custom directory is an installation
// the operator laid out on purpose, and several of them would share the one
// keychain account.
//
// Only an empty keychain receives the key, and the file is removed only
// after the keychain returned the same key. A keychain that already holds
// a different key is never overwritten — another installation may depend
// on it — and the file keeps being used.
func adoptFileKey(ks *KeychainStore, keyPath string, fileKey []byte) {
	if os.Getenv("CHATCLI_AUTH_DIR") != "" || ks == nil || !ks.IsNativeAvailable() {
		return
	}
	adoptMu.Lock()
	defer adoptMu.Unlock()
	if adoptDone[keyPath] {
		return
	}
	adoptDone[keyPath] = true

	held, err := lookupKeychainKey(ks)
	switch {
	case err == nil && subtle.ConstantTimeCompare(held, fileKey) == 1:
		markKeyInKeychain(keyPath)
		removeAdoptedKeyFile(keyPath)
	case err == nil:
		keychainWarnf("the OS keychain holds a different credential key than %s; keeping the file and leaving the keychain untouched", keyPath)
	case errors.Is(err, os.ErrNotExist):
		if storeKeyInKeychain(ks, fileKey) {
			markKeyInKeychain(keyPath)
			removeAdoptedKeyFile(keyPath)
		}
	}
	// Any other error: a locked or failing keychain; nothing moves.
}

// removeAdoptedKeyFile deletes the key file once the keychain holds it.
func removeAdoptedKeyFile(keyPath string) {
	if err := os.Remove(keyPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		keychainWarnf("credential key copied to the OS keychain, but %s could not be removed (%v)", keyPath, err)
	}
}

// strayFileKey resolves the auto backend when a key file exists but the
// marker says the key lives in the keychain: the file appeared after the
// move (a cron job with the file backend, a restored backup, a container
// with the directory mounted). The keychain key is the one the stores are
// sealed with, so it wins; the file is left for the user to inspect.
func strayFileKey(ks *KeychainStore, keyPath string, fileKey []byte) ([]byte, error) {
	held, err := lookupKeychainKey(ks)
	switch {
	case err == nil:
		if subtle.ConstantTimeCompare(held, fileKey) != 1 {
			keychainWarnf("%s does not hold the key this installation keeps in the OS keychain; using the keychain key", keyPath)
		}
		return held, nil
	case errors.Is(err, os.ErrNotExist) && ks.IsNativeAvailable():
		// The marker is stale (the item was removed by hand): the file is
		// the only key left, adopt it again.
		adoptFileKey(ks, keyPath, fileKey)
		return fileKey, nil
	case errors.Is(err, os.ErrNotExist):
		return nil, errKeyNotInKeychain(keyPath)
	default:
		return nil, keychainUnreadableError(err)
	}
}
