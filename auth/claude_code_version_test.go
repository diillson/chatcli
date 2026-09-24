/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package auth

import (
	"testing"
)

// The fingerprint never goes below the compiled floor, and only a
// well-formed, newer release moves it — from the env or from the API.
func TestEffectiveClaudeCodeVersion_FloorEnvAndLearned(t *testing.T) {
	t.Cleanup(ResetLearnedClaudeCodeVersion)
	ResetLearnedClaudeCodeVersion()
	t.Setenv(ClaudeCodeVersionEnv, "")
	if got := EffectiveClaudeCodeVersion(); got != ClaudeCodeVersion {
		t.Fatalf("effective = %q, want the floor %q", got, ClaudeCodeVersion)
	}
	if ClaudeCodeUA() != ClaudeCodeUserAgent {
		t.Fatalf("UA at the floor = %q, want %q", ClaudeCodeUA(), ClaudeCodeUserAgent)
	}

	// Env: older, malformed and equal values are ignored; newer wins.
	for _, stale := range []string{"1.0.0", "2.1.1", "banana", "2.1", ClaudeCodeVersion} {
		t.Setenv(ClaudeCodeVersionEnv, stale)
		if got := EffectiveClaudeCodeVersion(); got != ClaudeCodeVersion {
			t.Fatalf("env %q: effective = %q, want the floor", stale, got)
		}
	}
	t.Setenv(ClaudeCodeVersionEnv, "9.0.1")
	if got := EffectiveClaudeCodeVersion(); got != "9.0.1" {
		t.Fatalf("env override: effective = %q", got)
	}
	if got := ClaudeCodeUA(); got != "claude-cli/9.0.1 (external, cli)" {
		t.Fatalf("UA = %q", got)
	}
	t.Setenv(ClaudeCodeVersionEnv, "")

	// Learned: same rules; the newest of the three sources is presented.
	if AdoptClaudeCodeVersion("2.0.0") {
		t.Fatal("an older release must not be adopted")
	}
	if AdoptClaudeCodeVersion("2.1.x") {
		t.Fatal("a malformed release must not be adopted")
	}
	if !AdoptClaudeCodeVersion("2.9.300") {
		t.Fatal("a newer release is adopted")
	}
	if got := EffectiveClaudeCodeVersion(); got != "2.9.300" {
		t.Fatalf("learned: effective = %q", got)
	}
	if AdoptClaudeCodeVersion("2.9.300") {
		t.Fatal("adopting the same release again is a no-op")
	}
	t.Setenv(ClaudeCodeVersionEnv, "2.10.0")
	if got := EffectiveClaudeCodeVersion(); got != "2.10.0" {
		t.Fatalf("numeric order, not lexical: effective = %q", got)
	}
}

// The release the API asks for is read from the error it sends; any other
// 400 yields nothing.
func TestRequiredClaudeCodeVersion(t *testing.T) {
	body := `{"type":"error","error":{"type":"invalid_request_error","message":"Claude Code 2.1.259 does not support this model; version 2.1.280 or newer is required. Run 'claude update', or update the Claude desktop app, then try again.","details":{"error_code":"claude_code_version_too_old"}},"request_id":"req_1"}`
	if got := RequiredClaudeCodeVersion(body); got != "2.1.280" {
		t.Fatalf("required = %q", got)
	}
	if got := RequiredClaudeCodeVersion(`{"error":{"message":"version 2.1.280 or newer is required"}}`); got != "" {
		t.Fatalf("without the error code the message is not trusted: %q", got)
	}
	if got := RequiredClaudeCodeVersion(`{"error":{"details":{"error_code":"claude_code_version_too_old"},"message":"too old"}}`); got != "" {
		t.Fatalf("without a release in the message there is nothing to adopt: %q", got)
	}
	if got := RequiredClaudeCodeVersion(`{"error":{"type":"invalid_request_error","message":"max_tokens: too large"}}`); got != "" {
		t.Fatalf("unrelated 400: %q", got)
	}
}

func TestCompareRelease(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		sign int
	}{
		{"2.1.280", "2.1.259", 1}, {"2.1.259", "2.1.280", -1}, {"2.1.280", "2.1.280", 0},
		{"2.10.0", "2.9.9", 1}, {"3.0.0", "2.99.99", 1}, {"2.1", "2.1.0", -1},
	} {
		got := compareRelease(tc.a, tc.b)
		switch {
		case tc.sign > 0 && got <= 0, tc.sign < 0 && got >= 0, tc.sign == 0 && got != 0:
			t.Errorf("compareRelease(%q,%q) = %d, want sign %d", tc.a, tc.b, got, tc.sign)
		}
	}
}
