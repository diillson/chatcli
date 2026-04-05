/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package zai

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

// jwtCache caches generated JWT tokens to avoid regenerating on every request.
type jwtCache struct {
	mu        sync.RWMutex
	token     string
	expiresAt time.Time
}

// getToken returns the cached token if still valid (with safety margin), or empty string.
func (c *jwtCache) getToken(safetyMargin time.Duration) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.token != "" && time.Now().Add(safetyMargin).Before(c.expiresAt) {
		return c.token
	}
	return ""
}

// setToken stores a new token and its expiry time.
func (c *jwtCache) setToken(token string, expiresAt time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.token = token
	c.expiresAt = expiresAt
}

// generateZAIJWT generates a JWT token from a ZAI API key (format: id.secret).
// Returns the JWT token string, its expiry time, or error if the key format is invalid.
// TTL controls the token validity duration (ZAI allows up to 1 hour).
func generateZAIJWT(apiKey string, ttl time.Duration) (string, time.Time, error) {
	// Split id.secret
	parts := strings.SplitN(apiKey, ".", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", time.Time{}, fmt.Errorf("invalid ZAI API key format: expected 'id.secret'")
	}
	id, secret := parts[0], parts[1]

	now := time.Now()
	expiry := now.Add(ttl)

	// Header
	header := map[string]string{"alg": "HS256", "sign_type": "SIGN"}
	// Payload
	payload := map[string]interface{}{
		"api_key":   id,
		"exp":       expiry.UnixMilli(),
		"timestamp": now.UnixMilli(),
	}

	// Encode
	headerJSON, _ := json.Marshal(header)
	payloadJSON, _ := json.Marshal(payload)

	headerB64 := base64.RawURLEncoding.EncodeToString(headerJSON)
	payloadB64 := base64.RawURLEncoding.EncodeToString(payloadJSON)

	// Sign
	signingInput := headerB64 + "." + payloadB64
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(signingInput))
	signature := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))

	return signingInput + "." + signature, expiry, nil
}
