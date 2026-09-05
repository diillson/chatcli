/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package server

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"fmt"
	"strings"

	"github.com/diillson/chatcli/pkg/jwtkey"
)

// JWT algorithms this server verifies. Anything else — "none" first among
// them — is refused before a signature is ever computed.
const (
	jwtAlgHS256 = jwtkey.AlgHS256
	jwtAlgRS256 = jwtkey.AlgRS256
)

// verifyRS256 checks an RSASSA-PKCS1-v1_5 SHA-256 signature over
// "<header>.<payload>" against every trusted key.
func verifyRS256(signingInput string, signature []byte, keys []*rsa.PublicKey) bool {
	digest := sha256.Sum256([]byte(signingInput))
	for _, key := range keys {
		if key == nil {
			continue
		}
		if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], signature); err == nil {
			return true
		}
	}
	return false
}

// jwtHeaderAlg reads the "alg" field of a base64url-encoded JWT header.
//
// The algorithm is read and enforced against the one this server is
// configured for, rather than trusted to select the verification path.
// Letting the token pick would mean that a server holding an RSA public
// key also accepts an HS256 token signed with that public key as the HMAC
// secret — the classic algorithm-confusion forgery.
func jwtHeaderAlg(headerB64 string) (string, error) {
	b64URLReplacer := strings.NewReplacer("-", "+", "_", "/")
	raw, err := base64Decode(padBase64(b64URLReplacer.Replace(headerB64)))
	if err != nil {
		return "", fmt.Errorf("invalid token header encoding")
	}
	header, err := parseJSONClaims(raw)
	if err != nil {
		return "", fmt.Errorf("invalid token header")
	}
	alg := getStringClaim(header, "alg")
	if alg == "" {
		return "", fmt.Errorf("token header carries no alg")
	}
	return alg, nil
}
