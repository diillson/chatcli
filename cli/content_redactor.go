/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Secret redaction on the LLM-facing path.
 *
 * Everything the model receives that did not come from the user's own
 * keyboard — tool outputs in agent/coder mode, worker tool outputs, the
 * conversation segment handed to the memory extractor, and the @file/@git
 * context assembled in chat — passes through redactSecretsForLLM before it
 * leaves the process. Two layers compose:
 *
 *   1. KEY=VALUE lines (env dumps, .env files, docker/compose configs,
 *      CI logs) are judged by NAME through EnvRedactor — AWS_SECRET_ACCESS_KEY,
 *      DATABASE_URL, anything ending in _TOKEN/_PASSWORD/_KEY — plus its
 *      value heuristics (known prefixes, long hex).
 *   2. Free text is scanned by utils.SanitizeSensitiveText for the value
 *      shapes providers hand out (sk-…, ghp_…, AKIA…, JWTs, bearer headers,
 *      credential fields in JSON).
 *
 * CHATCLI_ENV_REDACT_MODE selects the policy: "permissive" (default) applies
 * both layers with the name denylist; "strict" additionally redacts every
 * KEY=VALUE line whose name is not on the known-safe allowlist; "off"
 * disables this chokepoint entirely (the pre-existing regex pass on exec
 * output is untouched by this setting).
 */
package cli

import (
	"os"
	"regexp"
	"strings"
	"sync"

	"github.com/diillson/chatcli/utils"
)

// contentRedactMode is the resolved CHATCLI_ENV_REDACT_MODE policy.
type contentRedactMode string

const (
	contentRedactOff        contentRedactMode = "off"
	contentRedactPermissive contentRedactMode = "permissive"
	contentRedactStrict     contentRedactMode = "strict"
)

// contentRedactModeFromEnv parses CHATCLI_ENV_REDACT_MODE. Unknown values
// (including the historical "normal"/"full" spellings) resolve to the
// permissive default so a typo never silently disables redaction.
func contentRedactModeFromEnv() contentRedactMode {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CHATCLI_ENV_REDACT_MODE"))) {
	case "off", "none", "disabled", "false", "0":
		return contentRedactOff
	case "strict":
		return contentRedactStrict
	default:
		return contentRedactPermissive
	}
}

// envAssignmentLine matches "KEY=VALUE" (optionally "export KEY=VALUE"),
// the shape env dumps, .env files and compose/CI logs share. Group 1 keeps
// the prefix and key verbatim so the redacted line stays parseable.
var envAssignmentLine = regexp.MustCompile(`^(\s*(?:export\s+)?[A-Za-z_][A-Za-z0-9_]*\s*=)(.*)$`)

// envRedactorPool caches one EnvRedactor per mode. The redactor's pattern
// tables are built from the environment at construction; CHATCLI_REDACT_PATTERNS
// is therefore read once per process, like every other boot-time table.
var (
	envRedactorMu   sync.Mutex
	envRedactorByMd = map[contentRedactMode]*EnvRedactor{}
)

func envRedactorFor(mode contentRedactMode) *EnvRedactor {
	envRedactorMu.Lock()
	defer envRedactorMu.Unlock()
	if r, ok := envRedactorByMd[mode]; ok {
		return r
	}
	r := NewEnvRedactor()
	if mode == contentRedactStrict {
		r.mode = EnvRedactStrict
	} else {
		r.mode = EnvRedactPermissive
	}
	envRedactorByMd[mode] = r
	return r
}

// redactSecretsForLLM returns text with secrets masked according to the
// configured policy. It is the single chokepoint for content that reaches
// the model from tools, files and the memory extractor; humans have already
// seen (and hooks already received) the unredacted original.
func redactSecretsForLLM(text string) string {
	mode := contentRedactModeFromEnv()
	if mode == contentRedactOff || text == "" {
		return text
	}
	return redactSecretsWithMode(text, mode)
}

// redactSecretsWithMode is the policy-explicit core, kept separate so tests
// can exercise every mode without touching the environment.
func redactSecretsWithMode(text string, mode contentRedactMode) string {
	if mode == contentRedactOff || text == "" {
		return text
	}
	original := text
	redactor := envRedactorFor(mode)

	// Layer 1: name-based KEY=VALUE redaction, line by line. Only lines
	// that match the assignment shape are touched, and only their value.
	if strings.Contains(text, "=") {
		lines := strings.Split(text, "\n")
		changed := false
		for i, line := range lines {
			m := envAssignmentLine.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			key := strings.TrimSpace(strings.TrimSuffix(m[1], "="))
			key = strings.TrimPrefix(key, "export ")
			key = strings.TrimSpace(key)
			value := strings.TrimSpace(m[2])
			if value == "" || value == "[REDACTED]" {
				continue
			}
			if redactor.isSensitive(key, strings.Trim(value, `"'`)) {
				lines[i] = m[1] + "[REDACTED]"
				changed = true
			}
		}
		if changed {
			text = strings.Join(lines, "\n")
		}
	}

	// Layer 2: value-shape scan over the whole text.
	out := utils.SanitizeSensitiveText(text)
	if out != original {
		redactionsTotal.Add(1)
	}
	return out
}
