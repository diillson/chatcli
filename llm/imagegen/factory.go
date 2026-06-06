/*
 * ChatCLI - Image generation provider factory.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Centralizes env reading. In `auto` mode (no CHATCLI_IMAGE_PROVIDER) the chosen
 * CHATCLI_IMAGE_MODEL decides the provider — e.g. `glm-image`/`cogview-*` → Z.AI,
 * `image-01` → MiniMax, `gpt-image-*` → OpenAI, `stability.*` → Bedrock — so a
 * user only picks a model (via /model-image) and the right backend + key are
 * used, no URL or provider env needed. Only an unrecognized/empty model falls
 * back to the key-presence chain below:
 *
 *   1. CHATCLI_IMAGE_PROVIDER=sdwebui → self-hosted Stable Diffusion WebUI at
 *      CHATCLI_IMAGE_URL (default http://localhost:7860). Keyless.
 *   2. CHATCLI_IMAGE_URL              → an OpenAI-compatible /images/generations
 *      endpoint. Keyless (unless CHATCLI_IMAGE_KEY is set).
 *   3. OPENAI_API_KEY                 → OpenAI images (paid).
 *   4. GOOGLEAI_API_KEY/GEMINI_API_KEY → native Google Imagen.
 *   5. XAI_API_KEY                    → native xAI grok-image.
 *   6. ZAI_API_KEY                    → Z.AI CogView-4 / GLM-Image.
 *   7. MINIMAX_API_KEY               → MiniMax Image-01.
 *   8. otherwise                      → Null (image generation disabled).
 *
 * CHATCLI_IMAGE_PROVIDER pins a backend (sdwebui|url|openai|google|xai|zai|
 * minimax|bedrock); a pinned backend whose config is missing degrades to Null
 * rather than silently switching. Beyond these, any provider speaking the
 * OpenAI image shape works by pointing CHATCLI_IMAGE_URL at it.
 */
package imagegen

import (
	"context"
	"os"
	"strconv"
	"strings"

	"go.uber.org/zap"
)

const (
	openAIBaseURL   = "https://api.openai.com/v1"
	xaiBaseURL      = "https://api.x.ai/v1"
	defaultXAIModel = "grok-2-image"

	// Z.AI (Zhipu) CogView / GLM-Image: OpenAI-shaped /images/generations, but
	// the response carries image URLs and the "n" field is rejected.
	zaiImageBaseURL      = "https://api.z.ai/api/paas/v4"
	defaultZAIImageModel = "glm-image"
)

// NewFromEnv builds the configured provider, falling back to Null when none is
// available. It never returns an error. Use NewFromEnvContext when a request
// context is available (the Bedrock backend honors it during AWS config load).
func NewFromEnv(logger *zap.Logger) Provider {
	return NewFromEnvContext(context.Background(), logger)
}

// NewFromEnvContext is NewFromEnv with a caller-supplied context, threaded into
// backends that perform setup I/O (Bedrock credential resolution).
func NewFromEnvContext(ctx context.Context, logger *zap.Logger) Provider {
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
		// CHATCLI_IMAGE_API=responses routes OpenAI through the Responses API
		// (a chat model like gpt-5.5 generates the image) instead of the
		// Images API (gpt-image-1).
		if openAIWantsResponses() {
			return responsesOrNull(model, logger, "CHATCLI_IMAGE_PROVIDER=openai + API=responses but OPENAI_API_KEY is empty")
		}
		return cloudOrNull(openAIBaseURL, os.Getenv("OPENAI_API_KEY"), model, "openai", logger,
			"CHATCLI_IMAGE_PROVIDER=openai set but OPENAI_API_KEY is empty")
	case "responses", "openai-responses":
		return responsesOrNull(model, logger, "CHATCLI_IMAGE_PROVIDER=responses but OPENAI_API_KEY is empty")
	case "google", "gemini", "imagen":
		return googleOrNull(model, logger, "CHATCLI_IMAGE_PROVIDER=google set but no GOOGLEAI_API_KEY/GEMINI_API_KEY")
	case "xai", "grok":
		return xaiOrNull(model, logger, "CHATCLI_IMAGE_PROVIDER=xai set but XAI_API_KEY is empty")
	case "zai", "zhipu", "glm", "cogview", "glm-image":
		return zaiOrNull(model, logger, "CHATCLI_IMAGE_PROVIDER=zai set but ZAI_API_KEY is empty")
	case "minimax", "hailuo":
		return minimaxOrNull(model, logger, "CHATCLI_IMAGE_PROVIDER=minimax set but MINIMAX_API_KEY is empty")
	case "bedrock", "aws":
		return bedrockOrNull(ctx, model, logger)
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
	// In auto mode the chosen MODEL decides the provider — picking a model is all
	// the user needs, no CHATCLI_IMAGE_PROVIDER. A recognized model that maps to
	// a provider whose credential is missing degrades to Null (with a pointed
	// warning) rather than silently routing the model to a backend that can't
	// run it. Only an unrecognized/empty model falls through to the key chain.
	switch providerFromModel(model) {
	case "openai":
		return cloudOrNull(openAIBaseURL, os.Getenv("OPENAI_API_KEY"), model, "openai", logger,
			"image model "+model+" implies OpenAI but OPENAI_API_KEY is empty")
	case "openai-responses":
		return responsesOrNull(model, logger,
			"image model "+model+" implies the OpenAI Responses API but OPENAI_API_KEY is empty")
	case "google":
		return googleOrNull(model, logger,
			"image model "+model+" implies Google but no GOOGLEAI_API_KEY/GEMINI_API_KEY")
	case "xai":
		return xaiOrNull(model, logger, "image model "+model+" implies xAI but XAI_API_KEY is empty")
	case "zai":
		return zaiOrNull(model, logger, "image model "+model+" implies Z.AI but ZAI_API_KEY is empty")
	case "minimax":
		return minimaxOrNull(model, logger, "image model "+model+" implies MiniMax but MINIMAX_API_KEY is empty")
	case "bedrock":
		return bedrockOrNull(ctx, model, logger)
	}
	// Cloud fallbacks: any provider whose key is present. OpenAI first for
	// back-compat, then Google (native Imagen) — so @image is not limited to a
	// single provider.
	if key := strings.TrimSpace(os.Getenv("OPENAI_API_KEY")); key != "" {
		if openAIWantsResponses() {
			return responsesOrNull(model, logger, "")
		}
		return cloudOrNull(openAIBaseURL, key, model, "openai", logger, "")
	}
	if googleImageKey() != "" {
		return googleOrNull(model, logger, "")
	}
	if strings.TrimSpace(os.Getenv("XAI_API_KEY")) != "" {
		return xaiOrNull(model, logger, "")
	}
	if strings.TrimSpace(os.Getenv("ZAI_API_KEY")) != "" {
		return zaiOrNull(model, logger, "")
	}
	if strings.TrimSpace(os.Getenv("MINIMAX_API_KEY")) != "" {
		return minimaxOrNull(model, logger, "")
	}
	return NewNull()
}

// bedrockOrNull builds the AWS Bedrock image backend (Nova Canvas / Titan),
// loading credentials from the standard AWS chain.
func bedrockOrNull(ctx context.Context, model string, logger *zap.Logger) Provider {
	p, err := NewBedrock(ctx, bedrockImageRegion(), bedrockImageProfile(), model, logger)
	if err != nil {
		logger.Warn("imagegen: Bedrock init failed; image generation disabled", zap.Error(err))
		return NewNull()
	}
	return p
}

// openAIWantsResponses reports whether the OpenAI path should use the Responses
// API (CHATCLI_IMAGE_API=responses) instead of the Images API.
func openAIWantsResponses() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("CHATCLI_IMAGE_API")))
	return v == "responses" || v == "response"
}

// responsesOrNull builds the OpenAI Responses-API backend (a chat model
// generates the image via the image_generation tool).
func responsesOrNull(model string, logger *zap.Logger, missingMsg string) Provider {
	key := strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
	if key == "" {
		if missingMsg != "" {
			logger.Warn("imagegen: " + missingMsg + "; image generation disabled")
		}
		return NewNull()
	}
	p, err := NewOpenAIResponses(openAIBaseURL, key, model, logger)
	if err != nil {
		logger.Warn("imagegen: Responses API init failed; image generation disabled", zap.Error(err))
		return NewNull()
	}
	return p
}

// xaiOrNull builds the xAI grok-image backend (OpenAI-shaped, but the size
// field is omitted because xAI rejects it).
func xaiOrNull(model string, logger *zap.Logger, missingMsg string) Provider {
	key := strings.TrimSpace(os.Getenv("XAI_API_KEY"))
	if key == "" {
		if missingMsg != "" {
			logger.Warn("imagegen: " + missingMsg + "; image generation disabled")
		}
		return NewNull()
	}
	m := model
	if m == "" {
		m = defaultXAIModel
	}
	p, err := NewOpenAICompatible(xaiBaseURL, key, m, "xai", logger)
	if err != nil {
		logger.Warn("imagegen: xAI init failed; image generation disabled", zap.Error(err))
		return NewNull()
	}
	p.omitSize = true
	return p
}

// zaiOrNull builds the Z.AI (Zhipu) image backend — CogView-4 / GLM-Image.
// They speak the OpenAI /images/generations shape, so the shared client serves
// them, but they return image URLs (downloaded automatically) and reject the
// "n" field. Reuses the same ZAI_API_KEY as the Z.AI chat provider.
func zaiOrNull(model string, logger *zap.Logger, missingMsg string) Provider {
	key := strings.TrimSpace(os.Getenv("ZAI_API_KEY"))
	if key == "" {
		if missingMsg != "" {
			logger.Warn("imagegen: " + missingMsg + "; image generation disabled")
		}
		return NewNull()
	}
	m := model
	if m == "" {
		m = defaultZAIImageModel
	}
	p, err := NewOpenAICompatible(zaiImageBaseURL, key, m, "zai", logger)
	if err != nil {
		logger.Warn("imagegen: Z.AI init failed; image generation disabled", zap.Error(err))
		return NewNull()
	}
	p.omitN = true
	return p
}

// minimaxOrNull builds the MiniMax (Hailuo) Image-01 backend (custom endpoint,
// base64 response). Reuses the same MINIMAX_API_KEY as the chat provider.
func minimaxOrNull(model string, logger *zap.Logger, missingMsg string) Provider {
	key := strings.TrimSpace(os.Getenv("MINIMAX_API_KEY"))
	if key == "" {
		if missingMsg != "" {
			logger.Warn("imagegen: " + missingMsg + "; image generation disabled")
		}
		return NewNull()
	}
	p, err := NewMiniMax(minimaxImageBaseURL, key, model, logger)
	if err != nil {
		logger.Warn("imagegen: MiniMax init failed; image generation disabled", zap.Error(err))
		return NewNull()
	}
	return p
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

// providerFromModel infers the image provider/backend from a model id so `auto`
// mode routes purely on the chosen model. It first consults the catalog (exact,
// case-insensitive match) — the self-maintaining source of truth — then falls
// back to id prefixes so dated/snapshot ids not yet in the catalog still
// resolve. Returns "" when the model maps to nothing recognizable. The OpenAI
// Responses-API models resolve to "openai-responses" so the caller picks the
// right OpenAI path without consulting CHATCLI_IMAGE_API.
func providerFromModel(model string) string {
	model = strings.ToLower(strings.TrimSpace(model))
	if model == "" {
		return ""
	}
	for _, m := range KnownModels() {
		if strings.ToLower(m.Name) == model {
			if m.Provider == "openai" && m.API == "responses" {
				return "openai-responses"
			}
			return m.Provider
		}
	}
	switch {
	case strings.HasPrefix(model, "stability."), strings.HasPrefix(model, "amazon."),
		strings.Contains(model, "nova-canvas"), strings.Contains(model, "titan-image"):
		return "bedrock"
	case strings.HasPrefix(model, "glm-image"), strings.HasPrefix(model, "cogview"), strings.HasPrefix(model, "glm-"):
		return "zai"
	case strings.HasPrefix(model, "image-0"), strings.HasPrefix(model, "minimax"), strings.HasPrefix(model, "hailuo"):
		return "minimax"
	case strings.HasPrefix(model, "grok"):
		return "xai"
	case strings.HasPrefix(model, "imagen"):
		return "google"
	case strings.HasPrefix(model, "gpt-image"), strings.HasPrefix(model, "dall-e"):
		return "openai"
	case strings.HasPrefix(model, "gpt-5"), strings.HasPrefix(model, "gpt-4"):
		return "openai-responses"
	}
	return ""
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
