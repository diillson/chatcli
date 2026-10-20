package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/pkg/stateroot"
)

const (
	encryptedPrefix = "chatcli-enc:v1:"
	keyFileName     = ".auth-key"
)

func getKeyPath() string {
	if dir := os.Getenv("CHATCLI_AUTH_DIR"); dir != "" {
		return filepath.Join(dir, keyFileName)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(stateroot.Under(home), keyFileName)
}

// loadOrCreateKey returns the key that encrypts stored credentials,
// creating one on first use.
//
// Where it lives is CHATCLI_KEYCHAIN_BACKEND's decision — see
// resolveEncryptionKey. Until this was wired, that variable was read and
// then ignored: the keychain store existed and nothing ever called it, so
// the key was always a file no matter what the setting said.
func loadOrCreateKey() ([]byte, error) {
	return resolveEncryptionKey(NewKeychainStore(), getKeyPath())
}

// newRandomKey generates a fresh 256-bit key.
func newRandomKey() ([]byte, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("%s: %w", i18n.T("auth.crypto.keygen_failed"), err)
	}
	return key, nil
}

// writeKeyFile persists the key with owner-only permissions. It never
// replaces an existing key file: see createKeyFile.
func writeKeyFile(keyPath string, key []byte) error {
	_, err := createKeyFile(keyPath, key)
	return err
}

// keyFileSettle bounds how long a process that lost the first-run race
// waits for the winner to finish writing its key.
const keyFileSettle = 2 * time.Second

// createKeyFile creates the key file holding key and returns the key now in
// effect. The file is created exclusively (O_CREATE|O_EXCL): when two
// processes start for the first time together, exactly one creates it and
// the other reads that key back instead of writing its own over it, so
// both encrypt with the same key.
func createKeyFile(keyPath string, key []byte) ([]byte, error) {
	dir := filepath.Dir(keyPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("%s: %w", i18n.T("auth.crypto.keydir_failed"), err)
	}
	// Publish atomically: the whole key goes to a private temp file first
	// and is then hard-linked into place. Link refuses an existing name (as
	// exclusive as O_EXCL) and the name only ever appears holding the
	// complete key, so a concurrent first run can never read an empty file
	// and fail closed on it.
	published, err := linkKeyFile(dir, keyPath, key)
	if err == nil {
		return published, nil
	}
	if !errors.Is(err, errNoHardLinks) {
		return nil, err
	}
	return createKeyFileExclusive(keyPath, key)
}

// errNoHardLinks reports a filesystem that cannot hard-link the key file.
var errNoHardLinks = errors.New("filesystem does not support hard links")

// linkFile publishes the key; a variable so tests can simulate filesystems
// without hard links.
var linkFile = os.Link

func linkKeyFile(dir, keyPath string, key []byte) ([]byte, error) {
	tmp, err := os.CreateTemp(dir, ".auth-key-*")
	if err != nil {
		return nil, fmt.Errorf("%s: %w", i18n.T("auth.crypto.keysave_failed"), err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	_, werr := tmp.Write(key)
	if serr := tmp.Sync(); werr == nil {
		werr = serr
	}
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Chmod(tmpName, 0o600)
	}
	if werr != nil {
		return nil, fmt.Errorf("%s: %w", i18n.T("auth.crypto.keysave_failed"), werr)
	}
	if err := linkFile(tmpName, filepath.Clean(keyPath)); err != nil {
		if errors.Is(err, os.ErrExist) {
			return awaitKeyFile(keyPath)
		}
		return nil, errNoHardLinks
	}
	return key, nil
}

// createKeyFileExclusive is the fallback for filesystems without hard
// links: exclusive create, then write.
func createKeyFileExclusive(keyPath string, key []byte) ([]byte, error) {
	f, err := os.OpenFile(filepath.Clean(keyPath), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //#nosec G304 -- key path derived from CHATCLI_AUTH_DIR or the home directory
	if errors.Is(err, os.ErrExist) {
		return awaitKeyFile(keyPath)
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", i18n.T("auth.crypto.keysave_failed"), err)
	}
	_, werr := f.Write(key)
	if serr := f.Sync(); werr == nil {
		werr = serr
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		// A partial key file would block every later start; remove it so
		// the next attempt can create a whole one.
		_ = os.Remove(keyPath)
		return nil, fmt.Errorf("%s: %w", i18n.T("auth.crypto.keysave_failed"), werr)
	}
	return key, nil
}

// awaitKeyFile reads the key another process just created, giving it a
// moment to finish writing.
func awaitKeyFile(keyPath string) ([]byte, error) {
	deadline := time.Now().Add(keyFileSettle)
	for {
		key, err := readKeyFileStrict(keyPath)
		if err == nil || time.Now().After(deadline) {
			return key, err
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func encryptData(plaintext []byte) ([]byte, error) {
	key, err := loadOrCreateKey()
	if err != nil {
		return nil, err
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", i18n.T("auth.crypto.cipher_failed"), err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", i18n.T("auth.crypto.gcm_failed"), err)
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("%s: %w", i18n.T("auth.crypto.nonce_failed"), err)
	}

	ciphertext := gcm.Seal(nonce, nonce, plaintext, nil)
	encoded := base64.StdEncoding.EncodeToString(ciphertext)

	return []byte(encryptedPrefix + encoded), nil
}

func decryptData(data []byte) ([]byte, error) {
	str := string(data)
	if !strings.HasPrefix(str, encryptedPrefix) {
		// Not encrypted — transparent migration from plaintext stores
		return data, nil
	}

	encoded := strings.TrimPrefix(str, encryptedPrefix)
	ciphertext, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", i18n.T("auth.crypto.decode_failed"), err)
	}

	key, err := loadOrCreateKey()
	if err != nil {
		return nil, err
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", i18n.T("auth.crypto.cipher_failed"), err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", i18n.T("auth.crypto.gcm_failed"), err)
	}

	nonceSize := gcm.NonceSize()
	if len(ciphertext) < nonceSize {
		return nil, fmt.Errorf("%s", i18n.T("auth.crypto.data_too_short"))
	}

	nonce, ciphertext := ciphertext[:nonceSize], ciphertext[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", i18n.T("auth.crypto.decrypt_failed"), err)
	}

	return plaintext, nil
}
