/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Subprocess environment scrub.
 *
 * MCP servers and executable plugins inherit the parent environment so
 * launchers (npx, uvx, docker) find their binaries and caches. That
 * inheritance also handed every child the secrets that protect ChatCLI's
 * own state: the encryption-at-rest master key, the server's JWT secret,
 * the auth directory and the audit trail path. A third-party server has
 * no business with any of them; a server that genuinely needs one gets it
 * through its own env map (opt-in passthrough).
 */
package utils

import "strings"

// SecretEnvDenylist are the variables never inherited by a subprocess:
// exact names and prefixes (a trailing '*' matches any suffix).
var SecretEnvDenylist = []string{
	"CHATCLI_ENCRYPTION_KEY*",
	"CHATCLI_JWT_SECRET",
	"CHATCLI_AUTH_DIR",
	"CHATCLI_AUDIT_LOG_PATH",
	"CHATCLI_MANAGED_CONFIG",
}

// IsSecretEnvKey reports whether key is on the denylist.
func IsSecretEnvKey(key string) bool {
	for _, d := range SecretEnvDenylist {
		if strings.HasSuffix(d, "*") {
			if strings.HasPrefix(key, strings.TrimSuffix(d, "*")) {
				return true
			}
			continue
		}
		if key == d {
			return true
		}
	}
	return false
}

// ScrubSecretEnv returns env without the denylisted variables, except the
// keys listed in allow (the caller's explicit passthrough).
func ScrubSecretEnv(env []string, allow map[string]string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		key, _, _ := strings.Cut(kv, "=")
		if IsSecretEnvKey(key) {
			if _, ok := allow[key]; !ok {
				continue
			}
		}
		out = append(out, kv)
	}
	return out
}
