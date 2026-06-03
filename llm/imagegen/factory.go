/*
 * ChatCLI - Image generation provider factory.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Centralizes env reading. Selection is "local/keyless first":
 *
 *   1. CHATCLI_IMAGE_PROVIDER=sdwebui → self-hosted Stable Diffusion WebUI at
 *      CHATCLI_IMAGE_URL (default http://localhost:7860). Keyless.
 *   2. CHATCLI_IMAGE_URL              → an OpenAI-compatible /images/generations
 *      endpoint. Keyless (unless CHATCLI_IMAGE_KEY is set).
 *   3. OPENAI_API_KEY                 → OpenAI images (paid).
 *   4. otherwise                      → Null (image generation disabled).
 *
 * CHATCLI_IMAGE_PROVIDER pins a backend (sdwebui|url|openai); a pinned backend
 * whose config is missing degrades to Null rather than silently switching.
 */
package imagegen

import (
	"os"
	"strconv"
	"strings"

	"go.uber.org/zap"
)

const openAIBaseURL = "https://api.openai.com/v1"

// NewFromEnv builds the configured provider, falling back to Null when none is
// available. It never returns an error.
func NewFromEnv(logger *zap.Logger) Provider {
	if logger == nil {
		logger = zap.NewNop()
	}
	url := strings.TrimSpace(os.Getenv("CHATCLI_IMAGE_URL"))
	model := strings.TrimSpace(os.Getenv("CHATCLI_IMAGE_MODEL"))

	switch strings.ToLower(strings.TrimSpace(os.Getenv("CHATCLI_IMAGE_PROVIDER"))) {
	case "sdwebui", "automatic1111", "sd":
		return sdOrNull(url, logger)
	case "url", "selfhosted":
		return selfHostedOrNull(url, model, logger)
	case "openai":
		return cloudOrNull(openAIBaseURL, os.Getenv("OPENAI_API_KEY"), model, "openai", logger,
			"CHATCLI_IMAGE_PROVIDER=openai set but OPENAI_API_KEY is empty")
	case "google", "gemini", "imagen":
		return googleOrNull(model, logger, "CHATCLI_IMAGE_PROVIDER=google set but no GOOGLEAI_API_KEY/GEMINI_API_KEY")
	case "", "auto":
		// fall through
	default:
		logger.Warn("imagegen: unknown CHATCLI_IMAGE_PROVIDER; image generation disabled",
			zap.String("value", os.Getenv("CHATCLI_IMAGE_PROVIDER")))
		return NewNull()
	}

	if url != "" {
		return selfHostedOrNull(url, model, logger)
	}
	// Cloud fallbacks: any provider whose key is present. OpenAI first for
	// back-compat, then Google (native Imagen) — so @image is not limited to a
	// single provider.
	if key := strings.TrimSpace(os.Getenv("OPENAI_API_KEY")); key != "" {
		return cloudOrNull(openAIBaseURL, key, model, "openai", logger, "")
	}
	if googleImageKey() != "" {
		return googleOrNull(model, logger, "")
	}
	return NewNull()
}

func googleOrNull(model string, logger *zap.Logger, missingMsg string) Provider {
	key := googleImageKey()
	if key == "" {
		if missingMsg != "" {
			logger.Warn("imagegen: " + missingMsg + "; image generation disabled")
		}
		return NewNull()
	}
	p, err := NewGoogle(key, model, logger)
	if err != nil {
		logger.Warn("imagegen: Google init failed; image generation disabled", zap.Error(err))
		return NewNull()
	}
	return p
}

func sdOrNull(url string, logger *zap.Logger) Provider {
	steps := 0
	if s := strings.TrimSpace(os.Getenv("CHATCLI_IMAGE_STEPS")); s != "" {
		steps, _ = strconv.Atoi(s)
	}
	p, err := NewAutomatic1111(url, steps, logger)
	if err != nil {
		logger.Warn("imagegen: invalid sdwebui config; image generation disabled", zap.Error(err))
		return NewNull()
	}
	return p
}

func selfHostedOrNull(url, model string, logger *zap.Logger) Provider {
	if strings.TrimSpace(url) == "" {
		logger.Warn("imagegen: self-hosted selected but CHATCLI_IMAGE_URL is empty; image generation disabled")
		return NewNull()
	}
	p, err := NewOpenAICompatible(url, os.Getenv("CHATCLI_IMAGE_KEY"), model, "selfhosted", logger)
	if err != nil {
		logger.Warn("imagegen: invalid CHATCLI_IMAGE_URL; image generation disabled", zap.Error(err))
		return NewNull()
	}
	return p
}

func cloudOrNull(baseURL, key, model, label string, logger *zap.Logger, missingMsg string) Provider {
	if strings.TrimSpace(key) == "" {
		if missingMsg != "" {
			logger.Warn("imagegen: " + missingMsg + "; image generation disabled")
		}
		return NewNull()
	}
	p, err := NewOpenAICompatible(baseURL, key, model, label, logger)
	if err != nil {
		logger.Warn("imagegen: provider init failed; image generation disabled", zap.Error(err))
		return NewNull()
	}
	return p
}
