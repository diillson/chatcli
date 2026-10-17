/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/diillson/chatcli/pkg/flock"
)

// previousKeychainAccount keeps the value MoveFileKeyToKeychain replaced:
// moving the key never destroys whatever the keychain held before.
const previousKeychainAccount = "auth-key.previous"

var (
	// ErrKeyFileNotProven reports a key file that does not decrypt the
	// credential store next to it, so it cannot be trusted over the
	// keychain's key.
	ErrKeyFileNotProven = errors.New("the key file does not open the credential store; nothing was moved")
	// ErrKeychainNotUsable reports a keychain that is unavailable here, or
	// a backend pinned to the file.
	ErrKeychainNotUsable = errors.New("no usable OS keychain here (or CHATCLI_KEYCHAIN_BACKEND=file); nothing was moved")
	// ErrCustomAuthDir reports an installation under CHATCLI_AUTH_DIR,
	// whose key never moves to the shared keychain account.
	ErrCustomAuthDir = errors.New("CHATCLI_AUTH_DIR is set; that installation keeps its key file")
)

// MoveFileKeyToKeychain moves the default key file into the OS keychain on
// request — the case the automatic move leaves alone because the keychain
// already holds a different key (typically left by an unrelated process).
// The file key must prove itself first by decrypting the credential store
// next to it; the keychain's previous value is kept under
// previousKeychainAccount; the key is read back before the file goes.
// The key itself does not change, so nothing sealed with it is affected.
// moved is false (with no error) when there is no key file to move.
//
// opensData, when given, receives the data secret derived from the file key
// and reports whether it opens the data already sealed at rest (true when
// there is none). It is the proof for an installation that never stored a
// credential: without auth-profiles.json, the encrypted stores are what the
// file key must open.
func MoveFileKeyToKeychain(opensData func(dataSecret string) bool) (moved bool, err error) {
	return moveKeyFileInto(NewKeychainStore(), getKeyPath(), opensData)
}

func moveKeyFileInto(ks *KeychainStore, keyPath string, opensData func(string) bool) (bool, error) {
	if os.Getenv("CHATCLI_AUTH_DIR") != "" {
		return false, ErrCustomAuthDir
	}
	if ks == nil || !ks.IsNativeAvailable() {
		return false, ErrKeychainNotUsable
	}
	unlock := flock.Lock(keyPath)
	defer unlock()

	fileKey, err := readKeyFileStrict(keyPath)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	held, err := lookupKeychainKey(ks)
	switch {
	case err == nil && subtle.ConstantTimeCompare(held, fileKey) == 1:
		markKeyInKeychain(keyPath)
		removeAdoptedKeyFile(keyPath)
		return true, nil
	case err == nil:
		if !fileKeyProven(fileKey, keyPath, opensData) {
			return false, ErrKeyFileNotProven
		}
		if serr := ks.Set(previousKeychainAccount, []byte(base64.StdEncoding.EncodeToString(held))); serr != nil {
			return false, serr
		}
	case !errors.Is(err, os.ErrNotExist):
		return false, keychainUnreadableError(err)
	}
	if !storeKeyInKeychain(ks, fileKey) {
		return false, ErrKeychainNotUsable
	}
	markKeyInKeychain(keyPath)
	removeAdoptedKeyFile(keyPath)
	return true, nil
}

// fileKeyProven reports whether the file key is the key this installation
// sealed its credentials and its data with: it must open the credential
// store when there is an encrypted one, and the sealed data stores.
func fileKeyProven(fileKey []byte, keyPath string, opensData func(string) bool) bool {
	if !keyOpensCredentialStore(fileKey, filepath.Join(filepath.Dir(keyPath), "auth-profiles.json")) {
		return false
	}
	if opensData == nil {
		return true
	}
	secret, err := deriveDataSecret(fileKey)
	return err == nil && opensData(secret)
}

// keyOpensCredentialStore reports whether key decrypts the credential store
// at path. An absent or plaintext store has nothing that depends on a key
// and proves the file key well enough.
func keyOpensCredentialStore(key []byte, path string) bool {
	data, err := os.ReadFile(filepath.Clean(path)) //#nosec G304 -- the credential store next to the key file
	if err != nil {
		return errors.Is(err, os.ErrNotExist)
	}
	str := string(data)
	if !strings.HasPrefix(str, encryptedPrefix) {
		return true
	}
	ciphertext, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(str, encryptedPrefix))
	if err != nil {
		return false
	}
	_, err = openWithKey(key, ciphertext)
	return err == nil
}

// openWithKey decrypts nonce||ciphertext with key (AES-256-GCM), the way
// decryptData does with the active key.
func openWithKey(key, ciphertext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(ciphertext) < gcm.NonceSize() {
		return nil, errors.New("ciphertext too short")
	}
	nonce, body := ciphertext[:gcm.NonceSize()], ciphertext[gcm.NonceSize():]
	return gcm.Open(nil, nonce, body, nil)
}
