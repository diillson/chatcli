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
	"strings"
	"testing"
	"time"
)

func TestGenerateZAIJWT_ValidKey(t *testing.T) {
	token, expiry, err := generateZAIJWT("myid.mysecret", 30*time.Minute)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Token must have 3 base64url parts
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("expected 3 JWT parts, got %d", len(parts))
	}

	// Verify each part is valid base64url
	for i, part := range parts {
		if _, err := base64.RawURLEncoding.DecodeString(part); err != nil {
			t.Errorf("part %d is not valid base64url: %v", i, err)
		}
	}

	// Verify header
	headerBytes, _ := base64.RawURLEncoding.DecodeString(parts[0])
	var header map[string]string
	if err := json.Unmarshal(headerBytes, &header); err != nil {
		t.Fatalf("failed to unmarshal header: %v", err)
	}
	if header["alg"] != "HS256" {
		t.Errorf("expected alg=HS256, got %s", header["alg"])
	}
	if header["sign_type"] != "SIGN" {
		t.Errorf("expected sign_type=SIGN, got %s", header["sign_type"])
	}

	// Verify payload contains api_key
	payloadBytes, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var payload map[string]interface{}
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		t.Fatalf("failed to unmarshal payload: %v", err)
	}
	if payload["api_key"] != "myid" {
		t.Errorf("expected api_key=myid, got %v", payload["api_key"])
	}
	if _, ok := payload["exp"]; !ok {
		t.Error("missing exp in payload")
	}
	if _, ok := payload["timestamp"]; !ok {
		t.Error("missing timestamp in payload")
	}

	// Verify expiry is in the future
	if !expiry.After(time.Now()) {
		t.Error("expiry should be in the future")
	}
}

func TestGenerateZAIJWT_InvalidKey(t *testing.T) {
	tests := []string{
		"nodotshere",
		".nosecret",
		"noid.",
		"",
		".",
	}
	for _, key := range tests {
		_, _, err := generateZAIJWT(key, 30*time.Minute)
		if err == nil {
			t.Errorf("expected error for key %q, got nil", key)
		}
	}
}

func TestGenerateZAIJWT_SignatureValid(t *testing.T) {
	secret := "testsecret123"
	apiKey := "testid." + secret

	token, _, err := generateZAIJWT(apiKey, 30*time.Minute)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("expected 3 parts, got %d", len(parts))
	}

	// Recompute signature
	signingInput := parts[0] + "." + parts[1]
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(signingInput))
	expectedSig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))

	if parts[2] != expectedSig {
		t.Errorf("signature mismatch:\n  got:      %s\n  expected: %s", parts[2], expectedSig)
	}
}

func TestJWTCache_Reuse(t *testing.T) {
	cache := &jwtCache{}
	apiKey := "testid.testsecret"

	// Generate and cache
	token1, expiry1, err := generateZAIJWT(apiKey, 30*time.Minute)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	cache.setToken(token1, expiry1)

	// Should return cached token (5-minute safety margin)
	cached := cache.getToken(5 * time.Minute)
	if cached == "" {
		t.Fatal("expected cached token, got empty")
	}
	if cached != token1 {
		t.Errorf("cached token differs from original")
	}
}

func TestJWTCache_Regenerate(t *testing.T) {
	cache := &jwtCache{}

	// Set a token that expires in 1 minute (within 5-min safety margin)
	expiredExpiry := time.Now().Add(1 * time.Minute)
	cache.setToken("old-token", expiredExpiry)

	// With 5-minute safety margin, this should return empty (needs regeneration)
	cached := cache.getToken(5 * time.Minute)
	if cached != "" {
		t.Errorf("expected empty (expired) token, got %q", cached)
	}

	// Generate a fresh one
	apiKey := "testid.testsecret"
	token2, expiry2, err := generateZAIJWT(apiKey, 30*time.Minute)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	cache.setToken(token2, expiry2)

	// Now should be available
	cached = cache.getToken(5 * time.Minute)
	if cached == "" {
		t.Fatal("expected new cached token, got empty")
	}
	if cached != token2 {
		t.Error("cached token differs from newly generated token")
	}
}
