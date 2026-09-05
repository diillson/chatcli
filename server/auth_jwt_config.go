/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package server

import (
	"os"
	"strings"

	"github.com/diillson/chatcli/pkg/jwtkey"
	"go.uber.org/zap"
)

// configureJWT selects the single JWT algorithm this server will accept.
//
// Two variables feed it, and the precedence is deliberate:
//
//   - CHATCLI_JWT_PUBLIC_KEY names an RSA public key (path or inline PEM)
//     and selects RS256 unambiguously. This is the variable to reach for.
//   - CHATCLI_JWT_SECRET is the HS256 shared secret, and — for the
//     deployments that were told a key path belongs there — also selects
//     RS256 when its value resolves to PEM key material.
//
// Exactly one algorithm ends up configured. A server never accepts both,
// because a verifier that accepts either is a verifier an attacker gets to
// choose for.
func (a *TokenAuthInterceptor) configureJWT(logger *zap.Logger) {
	if pub := strings.TrimSpace(os.Getenv("CHATCLI_JWT_PUBLIC_KEY")); pub != "" {
		keys, err := jwtkey.LoadRSAPublicKeys(pub)
		if err != nil {
			logger.Error("CHATCLI_JWT_PUBLIC_KEY is set but no RSA public key could be loaded; "+
				"JWT authentication stays disabled", zap.Error(err))
			return
		}
		a.jwtPublicKeys = keys
		a.jwtAlg = jwtAlgRS256
		logger.Info("JWT authentication enabled (RS256)", zap.Int("trusted_keys", len(keys)))
		return
	}

	secret := os.Getenv("CHATCLI_JWT_SECRET")
	if secret == "" {
		return
	}

	if jwtkey.LooksLikeKeyMaterial(secret) {
		keys, err := jwtkey.LoadRSAPublicKeys(secret)
		if err != nil {
			logger.Error("CHATCLI_JWT_SECRET looks like RSA key material but could not be loaded; "+
				"JWT authentication stays disabled. Set CHATCLI_JWT_PUBLIC_KEY for RS256, or a "+
				"plain shared secret for HS256", zap.Error(err))
			return
		}
		a.jwtPublicKeys = keys
		a.jwtAlg = jwtAlgRS256
		logger.Info("JWT authentication enabled (RS256 via CHATCLI_JWT_SECRET key material)",
			zap.Int("trusted_keys", len(keys)))
		return
	}

	a.jwtSecret = []byte(secret)
	a.jwtAlg = jwtAlgHS256
	logger.Info("JWT authentication enabled (HS256)")
}

// jwtConfigured reports whether any JWT verification material is loaded.
func (a *TokenAuthInterceptor) jwtConfigured() bool {
	return len(a.jwtSecret) > 0 || len(a.jwtPublicKeys) > 0
}
