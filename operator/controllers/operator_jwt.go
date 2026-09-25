/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package controllers

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/diillson/chatcli/pkg/jwtkey"
)

// Operator identity on the servers it drives. The role maps to the server's
// "user" level: the AIOps RPCs (alerts, analysis, remediation steps) need no
// more, and a leaked operator token must not administer the server.
const (
	operatorJWTSubject = "chatcli-operator"
	operatorJWTRole    = "operator"
	operatorJWTTTL     = time.Hour
	// operatorJWTRefreshAt is the share of the lifetime after which a new
	// token is minted, leaving headroom for clock skew and in-flight calls.
	operatorJWTRefreshAt = 0.75
)

// TokenSource yields the bearer credential for one outgoing call. An
// interface rather than a func type so ConnectionOpts stays comparable.
type TokenSource interface {
	Token(ctx context.Context) (string, error)
}

// jwtMinter issues short-lived HS256 tokens for the operator from the
// server's own signing secret (spec.server.security.jwtSecretRef) and
// caches each one until it is due for renewal.
type jwtMinter struct {
	secret   []byte
	issuer   string
	audience string
	ttl      time.Duration
	now      func() time.Time

	mu      sync.Mutex
	token   string
	expires time.Time
}

// newJWTMinter validates the material and returns a minter, or an error
// when the secret is RSA key material: that is a public key the server
// verifies RS256 tokens with, and the operator cannot sign with it.
func newJWTMinter(secret []byte, issuer, audience string) (*jwtMinter, error) {
	if len(secret) == 0 {
		return nil, errors.New("empty JWT secret")
	}
	if jwtkey.LooksLikeKeyMaterial(string(secret)) {
		return nil, errors.New("jwtSecretRef holds RSA key material: the server verifies RS256 tokens and the operator cannot sign them; set spec.server.security.operatorTokenRef")
	}
	return &jwtMinter{secret: secret, issuer: issuer, audience: audience, ttl: operatorJWTTTL, now: time.Now}, nil
}

// Token returns the cached token, minting a fresh one when none exists or
// the current one passed its renewal point.
func (m *jwtMinter) Token(_ context.Context) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	if m.token != "" && now.Before(m.expires) {
		return m.token, nil
	}
	exp := now.Add(m.ttl)
	tok, err := jwtkey.SignHS256(jwtkey.Claims{
		Subject:   operatorJWTSubject,
		Role:      operatorJWTRole,
		Issuer:    m.issuer,
		Audience:  m.audience,
		IssuedAt:  now.Unix(),
		ExpiresAt: exp.Unix(),
	}, m.secret)
	if err != nil {
		return "", fmt.Errorf("minting operator token: %w", err)
	}
	m.token = tok
	m.expires = now.Add(time.Duration(float64(m.ttl) * operatorJWTRefreshAt))
	return tok, nil
}

// staticToken presents one credential for the connection lifetime.
type staticToken string

// Token implements TokenSource.
func (s staticToken) Token(context.Context) (string, error) { return string(s), nil }

// staticTokenSource presents one credential for the connection lifetime.
func staticTokenSource(token string) TokenSource { return staticToken(token) }
