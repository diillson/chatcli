/*
 * ChatCLI - Transcription provider factory.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Centralizes env reading so callers never touch os.Getenv. The selection
 * order is "self-hosted first", matching the project's preference for keyless
 * / self-hosted backends:
 *
 *   1. CHATCLI_TRANSCRIPTION_URL set  → OpenAI-compatible at that URL. Keyless
 *      unless CHATCLI_TRANSCRIPTION_KEY is also provided. Covers whisper.cpp,
 *      faster-whisper, Speaches and any self-hosted server.
 *   2. CHATCLI_TRANSCRIPTION_PROVIDER=openai|groq → the matching cloud endpoint,
 *      requiring the provider's key. Missing key → disabled (no silent swap).
 *   3. No explicit choice but OPENAI_API_KEY present → OpenAI Whisper (zero-config).
 *   4. Otherwise → Null (voice input disabled).
 */
package transcription

import (
	"os"
	"strings"

	"go.uber.org/zap"
)

const (
	openAIBaseURL    = "https://api.openai.com/v1"
	groqBaseURL      = "https://api.groq.com/openai/v1"
	groqDefaultModel = "whisper-large-v3"
)

// NewFromEnv builds the configured provider, falling back to Null when none is
// available. It never returns an error: an unusable configuration degrades to
// Null so the gateway daemon keeps running and can tell the user voice is off.
func NewFromEnv(logger *zap.Logger) Provider {
	if logger == nil {
		logger = zap.NewNop()
	}
	model := strings.TrimSpace(os.Getenv("CHATCLI_TRANSCRIPTION_MODEL"))

	// 1. Self-hosted (or any explicit) endpoint by URL — keyless by default.
	if url := strings.TrimSpace(os.Getenv("CHATCLI_TRANSCRIPTION_URL")); url != "" {
		p, err := NewOpenAICompatible(url, os.Getenv("CHATCLI_TRANSCRIPTION_KEY"), model, "selfhosted", logger)
		if err != nil {
			logger.Warn("transcription: invalid CHATCLI_TRANSCRIPTION_URL; voice input disabled", zap.Error(err))
			return NewNull()
		}
		return p
	}

	// 2. Explicit cloud provider — requires its key.
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CHATCLI_TRANSCRIPTION_PROVIDER"))) {
	case "openai":
		return cloudOrNull(openAIBaseURL, os.Getenv("OPENAI_API_KEY"), model, "openai", logger,
			"CHATCLI_TRANSCRIPTION_PROVIDER=openai set but OPENAI_API_KEY is empty")
	case "groq":
		if model == "" {
			model = groqDefaultModel
		}
		return cloudOrNull(groqBaseURL, os.Getenv("GROQ_API_KEY"), model, "groq", logger,
			"CHATCLI_TRANSCRIPTION_PROVIDER=groq set but GROQ_API_KEY is empty")
	case "", "auto":
		// fall through to zero-config detection
	default:
		logger.Warn("transcription: unknown CHATCLI_TRANSCRIPTION_PROVIDER; voice input disabled",
			zap.String("value", os.Getenv("CHATCLI_TRANSCRIPTION_PROVIDER")))
		return NewNull()
	}

	// 3. Zero-config: reuse an already-configured OpenAI key.
	if key := strings.TrimSpace(os.Getenv("OPENAI_API_KEY")); key != "" {
		if p, err := NewOpenAICompatible(openAIBaseURL, key, model, "openai", logger); err == nil {
			return p
		}
	}

	// 4. Nothing configured.
	return NewNull()
}

// cloudOrNull builds a keyed cloud provider, or returns Null (logging why) when
// the key is missing — so an explicit but incomplete choice never silently
// falls back to a different backend.
func cloudOrNull(baseURL, key, model, label string, logger *zap.Logger, missingKeyMsg string) Provider {
	if strings.TrimSpace(key) == "" {
		logger.Warn("transcription: " + missingKeyMsg + "; voice input disabled")
		return NewNull()
	}
	p, err := NewOpenAICompatible(baseURL, key, model, label, logger)
	if err != nil {
		logger.Warn("transcription: provider init failed; voice input disabled", zap.Error(err))
		return NewNull()
	}
	return p
}
