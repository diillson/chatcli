/*
 * ChatCLI - Image model catalog.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * A small static catalog of image-capable models per backend, plus an optional
 * live GET against OpenAI's /v1/models so users can discover what's actually
 * available on their account — mirroring how the conversational side exposes a
 * catalog + dynamic listing.
 */
package imagegen

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/diillson/chatcli/utils"
	"go.uber.org/zap"
)

// ModelInfo describes one image-capable model.
type ModelInfo struct {
	Name     string // model id
	Provider string // openai | google | xai | sdwebui
	API      string // images | responses | native | local
	Note     string
}

// KnownModels returns the curated static catalog of image-capable models.
// Sourced from each provider's current docs (OpenAI image-generation guide,
// xAI image models, AWS Bedrock model catalog).
func KnownModels() []ModelInfo {
	return []ModelInfo{
		// OpenAI Images API (gpt-image family). May require org verification.
		// DALL-E 2/3 were retired (shut down 2026-05-12) — removed, they only
		// error now. The rest of the gpt-image family is deprecated in favor of
		// gpt-image-2: gpt-image-1 shuts down 2026-10-23; gpt-image-1.5,
		// gpt-image-1-mini and chatgpt-image-latest shut down 2026-12-01.
		{Name: "gpt-image-2", Provider: "openai", API: "images", Note: "Newest OpenAI image model — adds reasoning (Images API; default)."},
		{Name: "gpt-image-1.5", Provider: "openai", API: "images", Note: "OpenAI Images API (deprecated; shuts down 2026-12-01)."},
		{Name: "gpt-image-1", Provider: "openai", API: "images", Note: "OpenAI Images API (deprecated; shuts down 2026-10-23)."},
		{Name: "gpt-image-1-mini", Provider: "openai", API: "images", Note: "Smaller/cheaper OpenAI image model (deprecated; shuts down 2026-12-01)."},
		// OpenAI Responses API (a chat model generates via the image_generation
		// tool). gpt-5 base models are deprecated (shutdown 2026-12-11) —
		// replaced by the gpt-5.6 family (sol/terra/luna), all image-capable.
		{Name: "gpt-5.6-sol", Provider: "openai", API: "responses", Note: "OpenAI flagship w/ image_generation tool (Responses API; default)."},
		{Name: "gpt-5.6-terra", Provider: "openai", API: "responses", Note: "Mid-tier gpt-5.6 w/ image_generation tool (Responses API)."},
		{Name: "gpt-5.6-luna", Provider: "openai", API: "responses", Note: "Cheapest gpt-5.6 w/ image_generation tool (Responses API)."},
		{Name: "gpt-5.5", Provider: "openai", API: "responses", Note: "Chat model w/ image_generation tool (Responses API)."},
		// Google Gemini image models (:generateContent — text-to-image AND
		// editing, "Nano Banana"). The whole Imagen :predict family was shut
		// down on 2026-08-17 (Google's stated replacement is
		// gemini-3.1-flash-image) — Imagen entries removed, they only 404 now.
		{Name: "gemini-3.1-flash-image", Provider: "google", API: "gemini", Note: "Gemini 3.1 Flash Image / Nano Banana 2 (gen + edit; default)."},
		{Name: "gemini-3.1-flash-lite-image", Provider: "google", API: "gemini", Note: "Gemini 3.1 Flash-Lite Image (gen + edit; fastest/cheapest)."},
		{Name: "gemini-3-pro-image", Provider: "google", API: "gemini", Note: "Gemini 3 Pro Image (gen + edit; studio-quality 4K)."},
		{Name: "gemini-2.5-flash-image", Provider: "google", API: "gemini", Note: "Gemini 2.5 Flash Image (deprecated; shuts down 2026-10-02)."},
		// xAI. grok-2-image was retired and grok-imagine-image-quality is async
		// (it resets the connection on /images/generations → EOF) — removed.
		{Name: "grok-imagine-image-2.0", Provider: "xai", API: "native", Note: "xAI Grok Imagine 2.0 (newest; OpenAI-shaped /images/generations, no size; URL response; default)."},
		{Name: "grok-imagine-image", Provider: "xai", API: "native", Note: "xAI Grok Imagine (cheaper; OpenAI-shaped /images/generations, no size; URL response)."},
		// Z.AI (Zhipu) — OpenAI-shaped /images/generations, returns image URLs.
		// cogview-3-flash is China-only (open.bigmodel.cn) and not on api.z.ai — omitted.
		{Name: "glm-image", Provider: "zai", API: "images", Note: "Z.AI GLM-Image (newest; bilingual text-in-image; default)."},
		{Name: "cogview-4-250304", Provider: "zai", API: "images", Note: "Z.AI CogView-4 flagship (bilingual)."},
		// MiniMax (Hailuo) — custom /v1/image_generation endpoint, base64 response.
		{Name: "image-01", Provider: "minimax", API: "native", Note: "MiniMax Image-01 (text-to-image, up to 9 images)."},
		// AWS Bedrock (InvokeModel). Current Stability models use the text-to-image
		// shape; the Amazon TEXT_IMAGE models are legacy with no first-party
		// successor (the Nova 2 family has no image-gen model). Titan Image v2
		// hit EOL 2026-06-30 (requests fail) — removed.
		{Name: "stability.stable-image-core-v1:1", Provider: "bedrock", API: "stability", Note: "Bedrock Stability Stable Image Core (current; cheapest, fast; default)."},
		{Name: "stability.stable-image-ultra-v1:1", Provider: "bedrock", API: "stability", Note: "Bedrock Stability Stable Image Ultra (current; highest quality)."},
		{Name: "stability.sd3-5-large-v1:0", Provider: "bedrock", API: "stability", Note: "Bedrock Stability SD3.5 Large (current; flagship)."},
		{Name: "amazon.nova-canvas-v1:0", Provider: "bedrock", API: "native", Note: "Bedrock Nova Canvas (TEXT_IMAGE; LEGACY, EOL 2026-09-30)."},
	}
}

// FetchOpenAIModels lists models from the account via GET {base}/models and
// returns the ones whose id looks image-capable (gpt-image*, dall-e*, plus the
// gpt-5/4.1 family usable via the Responses API). Keyless callers get nil.
func FetchOpenAIModels(ctx context.Context, baseURL, apiKey string, logger *zap.Logger) ([]string, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, nil
	}
	if strings.TrimSpace(baseURL) == "" {
		baseURL = openAIBaseURL
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(apiKey))
	resp, err := utils.NewHTTPClientH1(logger, imageGenTimeout).Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrBody))
		return nil, &apiError{status: resp.StatusCode, body: strings.TrimSpace(string(snippet))}
	}
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	var ids []string
	for _, m := range out.Data {
		if isImageCapableID(m.ID) {
			ids = append(ids, m.ID)
		}
	}
	sort.Strings(ids)
	return ids, nil
}

func isImageCapableID(id string) bool {
	id = strings.ToLower(id)
	switch {
	case strings.Contains(id, "image"): // gpt-image-1, etc.
		return true
	case strings.HasPrefix(id, "dall-e"):
		return true
	case strings.HasPrefix(id, "gpt-5"), strings.HasPrefix(id, "gpt-4.1"), strings.HasPrefix(id, "gpt-4o"):
		return true // usable via the Responses API image_generation tool
	}
	return false
}

type apiError struct {
	status int
	body   string
}

func (e *apiError) Error() string {
	return "openai models API status " + itoa(e.status) + ": " + e.body
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
