/*
 * ChatCLI - Transcription provider factory.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Centralizes env reading so callers never touch os.Getenv. Selection is
 * "local/keyless first", matching the project's preference for self-hosted
 * backends (and the same local > groq(free) > openai(paid) order proven by
 * hermes-agent):
 *
 *   1. CHATCLI_TRANSCRIPTION_CMD  → a local STT command (whisper.cpp, etc.).
 *      Keyless, serverless.
 *   2. CHATCLI_TRANSCRIPTION_URL  → a self-hosted OpenAI-compatible endpoint.
 *      Keyless (unless CHATCLI_TRANSCRIPTION_KEY is set).
 *   3. a local whisper CLI on PATH (openai-whisper or whisper.cpp) → used
 *      automatically with zero config — "local by default" when installed.
 *   4. GROQ_API_KEY               → Groq Whisper (free tier).
 *   5. OPENAI_API_KEY             → OpenAI Whisper (paid).
 *   6. otherwise                  → Null (voice input disabled).
 *
 * CHATCLI_TRANSCRIPTION_PROVIDER pins a specific backend
 * (command|url|groq|openai); a pinned backend whose config is missing degrades
 * to Null rather than silently switching.
 */
package transcription

import (
	"os"
	"os/exec"
	"strings"

	"go.uber.org/zap"
)

const (
	openAIBaseURL    = "https://api.openai.com/v1"
	groqBaseURL      = "https://api.groq.com/openai/v1"
	groqDefaultModel = "whisper-large-v3"
)

// execLookPath is exec.LookPath, indirected so tests can simulate the presence
// or absence of a local whisper CLI deterministically.
var execLookPath = exec.LookPath

// NewFromEnv builds the configured provider, falling back to Null when none is
// available. It never returns an error: an unusable configuration degrades to
// Null so the gateway daemon keeps running and can tell the user voice is off.
func NewFromEnv(logger *zap.Logger) Provider {
	if logger == nil {
		logger = zap.NewNop()
	}
	model := strings.TrimSpace(os.Getenv("CHATCLI_TRANSCRIPTION_MODEL"))
	cmdTmpl := strings.TrimSpace(os.Getenv("CHATCLI_TRANSCRIPTION_CMD"))
	url := strings.TrimSpace(os.Getenv("CHATCLI_TRANSCRIPTION_URL"))

	switch strings.ToLower(strings.TrimSpace(os.Getenv("CHATCLI_TRANSCRIPTION_PROVIDER"))) {
	case "command", "local":
		return commandOrNull(cmdTmpl, logger, "CHATCLI_TRANSCRIPTION_PROVIDER=command set but CHATCLI_TRANSCRIPTION_CMD is empty")
	case "url", "selfhosted":
		return selfHostedOrNull(url, model, logger)
	case "openai":
		return cloudOrNull(openAIBaseURL, os.Getenv("OPENAI_API_KEY"), model, "openai", logger,
			"CHATCLI_TRANSCRIPTION_PROVIDER=openai set but OPENAI_API_KEY is empty")
	case "groq":
		return cloudOrNull(groqBaseURL, os.Getenv("GROQ_API_KEY"), groqModel(model), "groq", logger,
			"CHATCLI_TRANSCRIPTION_PROVIDER=groq set but GROQ_API_KEY is empty")
	case "", "auto":
		// fall through to local-first auto-detection
	default:
		logger.Warn("transcription: unknown CHATCLI_TRANSCRIPTION_PROVIDER; voice input disabled",
			zap.String("value", os.Getenv("CHATCLI_TRANSCRIPTION_PROVIDER")))
		return NewNull()
	}

	// Auto-detect, local/keyless first.
	if cmdTmpl != "" {
		p, err := NewCommandTranscriber(cmdTmpl, "command")
		if err == nil {
			return p
		}
		logger.Warn("transcription: invalid CHATCLI_TRANSCRIPTION_CMD; ignoring", zap.Error(err))
	}
	if url != "" {
		return selfHostedOrNull(url, model, logger)
	}
	// Local by default: if a whisper CLI is installed, use it with zero config
	// (keyless). This mirrors hermes-agent auto-using a local engine when present.
	if p := detectLocalWhisper(model, logger); p != nil {
		return p
	}
	if key := strings.TrimSpace(os.Getenv("GROQ_API_KEY")); key != "" {
		return cloudOrNull(groqBaseURL, key, groqModel(model), "groq", logger, "")
	}
	if key := strings.TrimSpace(os.Getenv("OPENAI_API_KEY")); key != "" {
		return cloudOrNull(openAIBaseURL, key, model, "openai", logger, "")
	}
	return NewNull()
}

// detectLocalWhisper returns a command-backed provider when a local whisper CLI
// is on PATH, or nil otherwise. openai-whisper (`whisper`) self-provisions its
// model and is preferred; whisper.cpp (`whisper-cli`) needs an explicit model
// file (CHATCLI_TRANSCRIPTION_MODEL or WHISPER_MODEL), so it is used only when
// one is configured.
func detectLocalWhisper(model string, logger *zap.Logger) Provider {
	if path, err := execLookPath("whisper"); err == nil && path != "" {
		size := model
		if size == "" || strings.Contains(size, "/") {
			size = "base" // a model size (base/small/medium/...), not a path
		}
		tmpl := "whisper {input} --model " + size + " --output_format txt --output_dir {output_dir} --task transcribe"
		if p, e := NewCommandTranscriber(tmpl, "local"); e == nil {
			logger.Info("transcription: using local openai-whisper", zap.String("model", size))
			return p
		}
	}
	if path, err := execLookPath("whisper-cli"); err == nil && path != "" {
		logger.Info("transcription: using local whisper.cpp (whisper-cli)")
		return newLocalWhisperCpp(path, model, logger)
	}
	return nil
}

// groqModel applies Groq's default whisper model when none is set.
func groqModel(model string) string {
	if model == "" {
		return groqDefaultModel
	}
	return model
}

// commandOrNull builds the local command backend, or Null (logging why) when no
// template is configured.
func commandOrNull(template string, logger *zap.Logger, missingMsg string) Provider {
	if strings.TrimSpace(template) == "" {
		logger.Warn("transcription: " + missingMsg + "; voice input disabled")
		return NewNull()
	}
	p, err := NewCommandTranscriber(template, "command")
	if err != nil {
		logger.Warn("transcription: invalid command template; voice input disabled", zap.Error(err))
		return NewNull()
	}
	return p
}

// selfHostedOrNull builds the self-hosted OpenAI-compatible backend, or Null
// when no URL is configured.
func selfHostedOrNull(url, model string, logger *zap.Logger) Provider {
	if strings.TrimSpace(url) == "" {
		logger.Warn("transcription: self-hosted selected but CHATCLI_TRANSCRIPTION_URL is empty; voice input disabled")
		return NewNull()
	}
	p, err := NewOpenAICompatible(url, os.Getenv("CHATCLI_TRANSCRIPTION_KEY"), model, "selfhosted", logger)
	if err != nil {
		logger.Warn("transcription: invalid CHATCLI_TRANSCRIPTION_URL; voice input disabled", zap.Error(err))
		return NewNull()
	}
	return p
}

// cloudOrNull builds a keyed cloud provider, or returns Null (logging missingMsg
// when non-empty) when the key is missing — so an explicit but incomplete choice
// never silently falls back to a different backend.
func cloudOrNull(baseURL, key, model, label string, logger *zap.Logger, missingMsg string) Provider {
	if strings.TrimSpace(key) == "" {
		if missingMsg != "" {
			logger.Warn("transcription: " + missingMsg + "; voice input disabled")
		}
		return NewNull()
	}
	p, err := NewOpenAICompatible(baseURL, key, model, label, logger)
	if err != nil {
		logger.Warn("transcription: provider init failed; voice input disabled", zap.Error(err))
		return NewNull()
	}
	return p
}
