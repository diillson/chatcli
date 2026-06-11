/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package utils

import (
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"os"
	"strings"
	"sync/atomic"

	"go.uber.org/zap"
)

// Global TLS trust overrides for corporate proxies that perform TLS
// interception with a private CA. They apply to every outbound HTTPS
// client in the process (LLM providers, TTS/STT, embeddings, web tools,
// gateway channels, MCP transports, registries), mirroring how
// NODE_EXTRA_CA_CERTS / NODE_TLS_REJECT_UNAUTHORIZED behave process-wide
// in Node.js tools such as Claude Code:
//
//   - CHATCLI_CA_BUNDLE=/path/to/pem
//     Merges the PEM into the system cert pool and uses it as RootCAs.
//     Go already trusts the OS cert store by default, so this is only
//     needed when the corporate CA cannot be installed system-wide.
//
//   - CHATCLI_TLS_INSECURE_SKIP_VERIFY=true
//     Disables TLS verification entirely. INSECURE — use only to confirm
//     a corporate-proxy issue, then switch to CHATCLI_CA_BUNDLE.
//
// Provider-specific overrides (CHATCLI_BEDROCK_CA_BUNDLE,
// CHATCLI_BEDROCK_INSECURE_SKIP_VERIFY) take precedence over these.
const (
	envCABundle           = "CHATCLI_CA_BUNDLE"
	envInsecureSkipVerify = "CHATCLI_TLS_INSECURE_SKIP_VERIFY"
)

// globalTLSConfig holds the trust overrides resolved by ApplyGlobalTLSTrust.
// nil means "no override": Go's defaults (system cert store) stay in effect.
var globalTLSConfig atomic.Pointer[tls.Config]

// GlobalTLSConfig returns the process-wide TLS overrides resolved by
// ApplyGlobalTLSTrust, or nil when none are configured. Callers that build
// their own transports should Clone() the result before mutating it.
func GlobalTLSConfig() *tls.Config {
	return globalTLSConfig.Load()
}

// ApplyGlobalTLSTrust resolves the CHATCLI_CA_BUNDLE /
// CHATCLI_TLS_INSECURE_SKIP_VERIFY overrides and wires them into both the
// transports built by NewHTTPClient and http.DefaultTransport — so bare
// &http.Client{} instances (gateway channels, MCP transports, registries,
// version check) inherit the corporate trust as well.
//
// Must run after the dotenv load so overrides set only in the .env file are
// seen. Idempotent; calling it with no overrides set is a no-op that leaves
// http.DefaultTransport untouched.
func ApplyGlobalTLSTrust(logger *zap.Logger) {
	cfg := newTLSConfigFromEnv(logger)
	globalTLSConfig.Store(cfg)
	if cfg == nil {
		return
	}
	if t, ok := http.DefaultTransport.(*http.Transport); ok {
		t.TLSClientConfig = cfg.Clone()
	}
}

// newTLSConfigFromEnv builds the override config from the environment.
// Returns nil when neither variable is set or the CA bundle is unusable
// (unreadable file, no valid certificates) — failing open to Go's default
// verification rather than silently weakening it.
func newTLSConfigFromEnv(logger *zap.Logger) *tls.Config {
	insecure := strings.EqualFold(strings.TrimSpace(os.Getenv(envInsecureSkipVerify)), "true")
	bundlePath := strings.TrimSpace(os.Getenv(envCABundle))

	if !insecure && bundlePath == "" {
		return nil
	}

	cfg := &tls.Config{MinVersion: tls.VersionTLS12}

	if insecure {
		// Explicit operator opt-in, mirroring NODE_TLS_REJECT_UNAUTHORIZED=0.
		// #nosec G402 -- documented escape hatch for corporate-proxy debugging
		cfg.InsecureSkipVerify = true
		logger.Warn(envInsecureSkipVerify + "=true — TLS verification is DISABLED for ALL outbound connections. " +
			"Do NOT use in production; configure " + envCABundle + " with your corporate CA instead.")
		return cfg
	}

	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	// The CA bundle path is intentionally operator-supplied via env var —
	// this is the documented way to trust a corporate proxy's CA.
	// #nosec G304 G703 -- user-controlled path by design (CHATCLI_CA_BUNDLE)
	pem, err := os.ReadFile(bundlePath)
	if err != nil {
		logger.Warn("failed to read "+envCABundle+"; keeping default TLS trust",
			zap.String("path", bundlePath), zap.Error(err))
		return nil
	}
	if !pool.AppendCertsFromPEM(pem) {
		logger.Warn("no valid certificates found in "+envCABundle+"; keeping default TLS trust",
			zap.String("path", bundlePath))
		return nil
	}
	cfg.RootCAs = pool
	logger.Info("using "+envCABundle+" for TLS trust on all outbound connections",
		zap.String("path", bundlePath))
	return cfg
}
