package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/diillson/chatcli/i18n"
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
	return filepath.Join(home, ".chatcli", keyFileName)
}

func loadOrCreateKey() ([]byte, error) {
	keyPath := getKeyPath()

	data, err := os.ReadFile(keyPath) //#nosec G304 -- path supplied by user/agent through validated tool surface (boundary check upstream)
	if err == nil && len(data) == 32 {
		return data, nil
	}

	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("%s: %w", i18n.T("auth.crypto.keygen_failed"), err)
	}

	dir := filepath.Dir(keyPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("%s: %w", i18n.T("auth.crypto.keydir_failed"), err)
	}

	if err := os.WriteFile(keyPath, key, 0o600); err != nil {
		return nil, fmt.Errorf("%s: %w", i18n.T("auth.crypto.keysave_failed"), err)
	}

	return key, nil
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
