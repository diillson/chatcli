package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
)

// PKCE contém verifier e challenge para OAuth PKCE flow.
type PKCE struct {
	Verifier  string
	Challenge string
}

// GeneratePKCE gera um par verifier/challenge para OAuth PKCE.
// Usa 32 bytes aleatórios para o verifier e SHA256 para o challenge.
func GeneratePKCE() (*PKCE, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return nil, err
	}

	verifier := base64URLEncode(bytes)

	hash := sha256.Sum256([]byte(verifier))
	challenge := base64URLEncode(hash[:])

	return &PKCE{
		Verifier:  verifier,
		Challenge: challenge,
	}, nil
}

// GenerateState gera um state aleatório para OAuth.
func GenerateState() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return base64URLEncode(bytes), nil
}

// base64URLEncode codifica bytes em base64 URL safe sem padding.
func base64URLEncode(data []byte) string {
	return base64.RawURLEncoding.EncodeToString(data)
}
