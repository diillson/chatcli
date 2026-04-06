/*
 * ChatCLI - Kubernetes Operator
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package controllers

import (
	"regexp"
	"strings"
	"sync"
)

// ScrubPattern is a named regex pattern for sensitive data detection.
type ScrubPattern struct {
	Name    string
	Pattern *regexp.Regexp
}

// LogScrubber sanitizes log content by replacing sensitive data with redaction markers.
// Thread-safe for concurrent use.
type LogScrubber struct {
	mu       sync.RWMutex
	patterns []ScrubPattern
}

// DefaultScrubPatterns returns production-grade patterns for sensitive data detection.
func DefaultScrubPatterns() []ScrubPattern {
	return []ScrubPattern{
		// AWS Access Keys (start with AKIA, 20 chars)
		{Name: "aws_key", Pattern: regexp.MustCompile(`AKIA[0-9A-Z]{16}`)},
		// AWS Secret Keys (40 chars base64-like)
		{Name: "aws_secret", Pattern: regexp.MustCompile(`(?i)(aws_secret_access_key|aws_secret)\s*[=:]\s*[A-Za-z0-9/+=]{40}`)},
		// JWT tokens (3 base64url parts separated by dots)
		{Name: "jwt", Pattern: regexp.MustCompile(`eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}`)},
		// Bearer tokens in headers/logs
		{Name: "bearer", Pattern: regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9_\-.~+/]+=*`)},
		// Generic API keys (key=value patterns)
		{Name: "api_key", Pattern: regexp.MustCompile(`(?i)(api[_-]?key|apikey|access[_-]?key)\s*[=:]\s*[^\s,;]{8,}`)},
		// Password in connection strings
		{Name: "password_conn", Pattern: regexp.MustCompile(`(?i)(password|passwd|pwd)\s*[=:]\s*[^\s,;@]{3,}`)},
		// Database connection strings with credentials
		{Name: "db_uri", Pattern: regexp.MustCompile(`(?i)(postgres|mysql|mongodb|redis|amqp)://[^:]+:[^@]+@`)},
		// Generic token patterns
		{Name: "token", Pattern: regexp.MustCompile(`(?i)(token|secret)\s*[=:]\s*[^\s,;]{8,}`)},
		// Kubernetes service account tokens
		{Name: "k8s_sa_token", Pattern: regexp.MustCompile(`eyJhbGciOi[A-Za-z0-9_-]{50,}`)},
		// GitHub tokens
		{Name: "github_token", Pattern: regexp.MustCompile(`gh[ps]_[A-Za-z0-9_]{36,}`)},
		{Name: "github_pat", Pattern: regexp.MustCompile(`github_pat_[A-Za-z0-9_]{22,}`)},
		// Slack tokens
		{Name: "slack_token", Pattern: regexp.MustCompile(`xox[bpoa]-[0-9]{10,}-[A-Za-z0-9-]+`)},
		// OpenAI keys
		{Name: "openai_key", Pattern: regexp.MustCompile(`sk-[A-Za-z0-9]{20,}`)},
		// Private keys (PEM blocks)
		{Name: "private_key", Pattern: regexp.MustCompile(`-----BEGIN\s+(RSA\s+)?PRIVATE\s+KEY-----`)},
		// IPv4 addresses (optional — configurable)
		{Name: "ipv4", Pattern: regexp.MustCompile(`\b\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}\b`)},
		// Email addresses
		{Name: "email", Pattern: regexp.MustCompile(`[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}`)},
		// Base64-encoded secrets (long base64 strings, likely encoded creds)
		{Name: "base64_secret", Pattern: regexp.MustCompile(`(?:[A-Za-z0-9+/]{4}){15,}={0,2}`)},
		// Hex-encoded secrets (64+ chars of hex)
		{Name: "hex_secret", Pattern: regexp.MustCompile(`[0-9a-fA-F]{64,}`)},
	}
}

// NewLogScrubber creates a scrubber with default patterns plus optional custom ones.
// additionalPatterns is a comma-separated list of regex patterns from configuration.
func NewLogScrubber(additionalPatterns string) *LogScrubber {
	ls := &LogScrubber{
		patterns: DefaultScrubPatterns(),
	}

	// Parse additional custom patterns
	if additionalPatterns != "" {
		for _, p := range strings.Split(additionalPatterns, ",") {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			compiled, err := regexp.Compile(p)
			if err != nil {
				continue // skip invalid patterns
			}
			ls.patterns = append(ls.patterns, ScrubPattern{
				Name:    "custom",
				Pattern: compiled,
			})
		}
	}

	return ls
}

// ScrubText replaces all sensitive data in the text with [REDACTED:type] markers.
func (ls *LogScrubber) ScrubText(text string) string {
	ls.mu.RLock()
	defer ls.mu.RUnlock()

	result := text
	for _, sp := range ls.patterns {
		redaction := "[REDACTED:" + sp.Name + "]"
		result = sp.Pattern.ReplaceAllString(result, redaction)
	}
	return result
}

// AddPatterns adds custom scrub patterns at runtime.
func (ls *LogScrubber) AddPatterns(patterns []ScrubPattern) {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	ls.patterns = append(ls.patterns, patterns...)
}

// PatternCount returns the number of active scrub patterns.
func (ls *LogScrubber) PatternCount() int {
	ls.mu.RLock()
	defer ls.mu.RUnlock()
	return len(ls.patterns)
}
