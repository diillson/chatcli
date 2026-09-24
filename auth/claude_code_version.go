/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * The Claude Code fingerprint the OAuth surface presents is a moving
 * target: the Messages API gates each new model on a minimum Claude Code
 * release ("Claude Code X does not support this model; version Y or newer
 * is required", error_code claude_code_version_too_old). The compiled
 * constant (ClaudeCodeVersion) is the floor; this file lets the process
 * present a newer release without waiting for a ChatCLI build — from the
 * CHATCLI_CLAUDE_CODE_VERSION env, or learned from that very error so the
 * request that hit it can be sent again.
 */
package auth

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
)

// ClaudeCodeVersionEnv overrides the Claude Code release the OAuth surface
// presents (a release triple such as 2.1.281). Lower or malformed values
// are ignored: the fingerprint never goes below the compiled floor.
const ClaudeCodeVersionEnv = "CHATCLI_CLAUDE_CODE_VERSION"

// claudeCodeVersionLearned is the release a claude_code_version_too_old
// error asked for, once adopted for this process (empty until then).
var claudeCodeVersionLearned atomic.Value

// releaseTriple matches a Claude Code release such as 2.1.280.
var releaseTriple = regexp.MustCompile(`^\d+\.\d+\.\d+$`)

// requiredVersionPattern finds the release the API asks for in the
// claude_code_version_too_old message.
var requiredVersionPattern = regexp.MustCompile(`version\s+(\d+\.\d+\.\d+)\s+or\s+newer\s+is\s+required`)

// EffectiveClaudeCodeVersion is the release the process presents: the
// newest of the compiled floor, the env override and the release learned
// from the API.
func EffectiveClaudeCodeVersion() string {
	v := ClaudeCodeVersion
	if env := strings.TrimSpace(os.Getenv(ClaudeCodeVersionEnv)); releaseTriple.MatchString(env) && compareRelease(env, v) > 0 {
		v = env
	}
	if learned, _ := claudeCodeVersionLearned.Load().(string); learned != "" && compareRelease(learned, v) > 0 {
		v = learned
	}
	return v
}

// ClaudeCodeUA is the User-Agent of the OAuth surface for the effective
// release; ClaudeCodeUserAgent is the same string for the compiled floor.
func ClaudeCodeUA() string {
	return "claude-cli/" + EffectiveClaudeCodeVersion() + " (external, cli)"
}

// AdoptClaudeCodeVersion makes the process present the given release when
// it is a well-formed triple newer than the effective one. It reports
// whether the fingerprint changed, which is what makes a resend worth it.
func AdoptClaudeCodeVersion(version string) bool {
	version = strings.TrimSpace(version)
	if !releaseTriple.MatchString(version) || compareRelease(version, EffectiveClaudeCodeVersion()) <= 0 {
		return false
	}
	claudeCodeVersionLearned.Store(version)
	return true
}

// ResetLearnedClaudeCodeVersion forgets the release learned from the API.
// Tests use it; the process itself never needs to go back.
func ResetLearnedClaudeCodeVersion() {
	claudeCodeVersionLearned.Store("")
}

// RequiredClaudeCodeVersion reads the release a claude_code_version_too_old
// error body asks for. It returns "" when the body is any other error.
func RequiredClaudeCodeVersion(body string) string {
	if !strings.Contains(body, "claude_code_version_too_old") {
		return ""
	}
	m := requiredVersionPattern.FindStringSubmatch(body)
	if m == nil {
		return ""
	}
	return m[1]
}

// compareRelease orders two release triples numerically: negative when a
// is older than b, zero when equal, positive when newer.
func compareRelease(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < 3 && i < len(as) && i < len(bs); i++ {
		ai, _ := strconv.Atoi(as[i])
		bi, _ := strconv.Atoi(bs[i])
		if ai != bi {
			return ai - bi
		}
	}
	return len(as) - len(bs)
}
