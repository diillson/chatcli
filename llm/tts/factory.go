/*
 * ChatCLI - TTS provider factory.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Centralizes env reading so callers never touch os.Getenv. Selection is
 * "local/keyless first", matching the project's preference for self-hosted
 * backends:
 *
 *   1. CHATCLI_TTS_CMD   → a local TTS command template. Keyless, serverless.
 *   2. CHATCLI_TTS_URL   → a self-hosted OpenAI-compatible /audio/speech
 *      endpoint. Keyless (unless CHATCLI_TTS_KEY is set).
 *   3. a local TTS CLI on PATH (macOS `say`, espeak-ng, espeak) → used
 *      automatically with zero config — "local by default" when installed.
 *   4. OPENAI_API_KEY    → OpenAI TTS (paid).
 *   5. otherwise         → Null (voice output disabled).
 *
 * CHATCLI_TTS_PROVIDER pins a specific backend (command|url|openai); a pinned
 * backend whose config is missing degrades to Null rather than silently
 * switching.
 */
package tts

import (
	"os"
	"os/exec"
	"strings"

	"go.uber.org/zap"
)

const openAIBaseURL = "https://api.openai.com/v1"

// execLookPath is exec.LookPath, indirected for deterministic tests.
var execLookPath = exec.LookPath

// NewFromEnv builds the configured provider, falling back to Null when none is
// available. It never returns an error: an unusable configuration degrades to
// Null so the daemon keeps running and can tell the user voice output is off.
func NewFromEnv(logger *zap.Logger) Provider {
	if logger == nil {
		logger = zap.NewNop()
	}
	model := strings.TrimSpace(os.Getenv("CHATCLI_TTS_MODEL"))
	cmdTmpl := strings.TrimSpace(os.Getenv("CHATCLI_TTS_CMD"))
	url := strings.TrimSpace(os.Getenv("CHATCLI_TTS_URL"))

	switch strings.ToLower(strings.TrimSpace(os.Getenv("CHATCLI_TTS_PROVIDER"))) {
	case "command", "local":
		return commandOrNull(cmdTmpl, logger, "CHATCLI_TTS_PROVIDER=command set but CHATCLI_TTS_CMD is empty")
	case "url", "selfhosted":
		return selfHostedOrNull(url, model, logger)
	case "openai":
		return cloudOrNull(openAIBaseURL, os.Getenv("OPENAI_API_KEY"), model, "openai", logger,
			"CHATCLI_TTS_PROVIDER=openai set but OPENAI_API_KEY is empty")
	case "", "auto":
		// fall through to local-first auto-detection
	default:
		logger.Warn("tts: unknown CHATCLI_TTS_PROVIDER; voice output disabled",
			zap.String("value", os.Getenv("CHATCLI_TTS_PROVIDER")))
		return NewNull()
	}

	if cmdTmpl != "" {
		if p, err := NewCommandSynthesizer(cmdTmpl, ttsCmdExt(), "command"); err == nil {
			return p
		} else {
			logger.Warn("tts: invalid CHATCLI_TTS_CMD; ignoring", zap.Error(err))
		}
	}
	if url != "" {
		return selfHostedOrNull(url, model, logger)
	}
	if p := detectLocalTTS(logger); p != nil {
		return p
	}
	if key := strings.TrimSpace(os.Getenv("OPENAI_API_KEY")); key != "" {
		return cloudOrNull(openAIBaseURL, key, model, "openai", logger, "")
	}
	return NewNull()
}

// detectLocalTTS returns a command-backed provider when a local TTS CLI is on
// PATH, or nil otherwise. macOS `say` is preferred; espeak-ng / espeak follow.
func detectLocalTTS(logger *zap.Logger) Provider {
	if path, err := execLookPath("say"); err == nil && path != "" {
		if p, e := NewCommandSynthesizer("say {text} -o {output}", "aiff", "local"); e == nil {
			logger.Info("tts: using local macOS `say`")
			return p
		}
	}
	for _, bin := range []string{"espeak-ng", "espeak"} {
		if path, err := execLookPath(bin); err == nil && path != "" {
			if p, e := NewCommandSynthesizer(bin+" {text} -w {output}", "wav", "local"); e == nil {
				logger.Info("tts: using local " + bin)
				return p
			}
		}
	}
	return nil
}

// ttsCmdExt picks the output extension for a user-supplied CHATCLI_TTS_CMD.
// CHATCLI_TTS_CMD_EXT overrides; default wav.
func ttsCmdExt() string {
	if ext := strings.TrimSpace(os.Getenv("CHATCLI_TTS_CMD_EXT")); ext != "" {
		return ext
	}
	return "wav"
}

func commandOrNull(template string, logger *zap.Logger, missingMsg string) Provider {
	if strings.TrimSpace(template) == "" {
		logger.Warn("tts: " + missingMsg + "; voice output disabled")
		return NewNull()
	}
	p, err := NewCommandSynthesizer(template, ttsCmdExt(), "command")
	if err != nil {
		logger.Warn("tts: invalid command template; voice output disabled", zap.Error(err))
		return NewNull()
	}
	return p
}

func selfHostedOrNull(url, model string, logger *zap.Logger) Provider {
	if strings.TrimSpace(url) == "" {
		logger.Warn("tts: self-hosted selected but CHATCLI_TTS_URL is empty; voice output disabled")
		return NewNull()
	}
	p, err := NewOpenAICompatible(url, os.Getenv("CHATCLI_TTS_KEY"), model, "selfhosted", logger)
	if err != nil {
		logger.Warn("tts: invalid CHATCLI_TTS_URL; voice output disabled", zap.Error(err))
		return NewNull()
	}
	return p
}

func cloudOrNull(baseURL, key, model, label string, logger *zap.Logger, missingMsg string) Provider {
	if strings.TrimSpace(key) == "" {
		if missingMsg != "" {
			logger.Warn("tts: " + missingMsg + "; voice output disabled")
		}
		return NewNull()
	}
	p, err := NewOpenAICompatible(baseURL, key, model, label, logger)
	if err != nil {
		logger.Warn("tts: provider init failed; voice output disabled", zap.Error(err))
		return NewNull()
	}
	return p
}
