/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Persisted-data redaction.
 *
 * Secret redaction always runs on the LLM-bound path. What ChatCLI writes
 * to disk for itself — sessions, the transcript journal, CCR archives,
 * hub mirrors — kept the raw text, so a token pasted once lived on in
 * every store. The strict policy (CHATCLI_ENV_REDACT_MODE=strict, the
 * existing switch) now also masks what is persisted; the permissive
 * default keeps stores verbatim so /rewind and exports stay faithful.
 */
package cli

import "github.com/diillson/chatcli/models"

// persistRedactEnabled reports whether persisted stores are redacted.
func persistRedactEnabled() bool { return contentRedactModeFromEnv() == contentRedactStrict }

// persistRedact masks secrets in text bound for a persistent store under
// the strict policy; identity otherwise.
func persistRedact(text string) string {
	if text == "" || !persistRedactEnabled() {
		return text
	}
	return redactSecretsForLLM(text)
}

// persistRedactMessages returns a redacted copy of msgs under the strict
// policy (the live history is never touched); the same slice otherwise.
func persistRedactMessages(msgs []models.Message) []models.Message {
	if len(msgs) == 0 || !persistRedactEnabled() {
		return msgs
	}
	out := make([]models.Message, len(msgs))
	for i, m := range msgs {
		out[i] = m
		out[i].Content = redactSecretsForLLM(m.Content)
	}
	return out
}
