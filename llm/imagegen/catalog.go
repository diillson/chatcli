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
		// DALL-E 2/3 were retired (dall-e-3 on 2026-03-04; both shut down
		// 2026-05-12) — removed, they only error now. Use the gpt-image family.
		{Name: "gpt-image-2", Provider: "openai", API: "images", Note: "Newest OpenAI image model — adds reasoning (Images API)."},
		{Name: "gpt-image-1.5", Provider: "openai", API: "images", Note: "OpenAI Images API."},
		{Name: "gpt-image-1", Provider: "openai", API: "images", Note: "OpenAI Images API (broadly available; default)."},
		{Name: "gpt-image-1-mini", Provider: "openai", API: "images", Note: "Smaller/cheaper OpenAI image model."},
		// OpenAI Responses API (a chat model generates via the image_generation tool).
		{Name: "gpt-5.5", Provider: "openai", API: "responses", Note: "Chat model w/ image_generation tool (Responses API)."},
		{Name: "gpt-5", Provider: "openai", API: "responses", Note: "gpt-5 and newer support the image_generation tool."},
		// Google.
		{Name: "imagen-3.0-generate-002", Provider: "google", API: "native", Note: "Google Imagen (predict)."},
		// xAI.
		{Name: "grok-2-image", Provider: "xai", API: "native", Note: "xAI image (OpenAI-shaped /images/generations, no size)."},
		{Name: "grok-imagine-image-quality", Provider: "xai", API: "imagine", Note: "xAI Imagine quality model (Imagine API)."},
		// Z.AI (Zhipu) — OpenAI-shaped /images/generations, returns image URLs.
		{Name: "glm-image", Provider: "zai", API: "images", Note: "Z.AI GLM-Image (newest; bilingual text-in-image; default)."},
		{Name: "cogview-4-250304", Provider: "zai", API: "images", Note: "Z.AI CogView-4 flagship (bilingual)."},
		{Name: "cogview-3-flash", Provider: "zai", API: "images", Note: "Z.AI CogView-3 Flash (free tier)."},
		// MiniMax (Hailuo) — custom /v1/image_generation endpoint, base64 response.
		{Name: "image-01", Provider: "minimax", API: "native", Note: "MiniMax Image-01 (text-to-image, up to 9 images)."},
		// AWS Bedrock (InvokeModel). Current Stability models use the text-to-image
		// shape; the Amazon TEXT_IMAGE models are legacy (AWS recommends migrating
		// to Stability Core/Ultra/SD3.5 — Nova Canvas EOL 2026-09-30).
		{Name: "stability.stable-image-core-v1:1", Provider: "bedrock", API: "stability", Note: "Bedrock Stability Stable Image Core (current; cheapest, fast; default)."},
		{Name: "stability.stable-image-ultra-v1:1", Provider: "bedrock", API: "stability", Note: "Bedrock Stability Stable Image Ultra (current; highest quality)."},
		{Name: "stability.sd3-5-large-v1:0", Provider: "bedrock", API: "stability", Note: "Bedrock Stability SD3.5 Large (current; flagship)."},
		{Name: "amazon.nova-canvas-v1:0", Provider: "bedrock", API: "native", Note: "Bedrock Nova Canvas (TEXT_IMAGE; LEGACY, EOL 2026-09-30)."},
		{Name: "amazon.titan-image-generator-v2:0", Provider: "bedrock", API: "native", Note: "Bedrock Titan Image v2 (TEXT_IMAGE; legacy)."},
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
	resp, err := utils.NewHTTPClient(logger, imageGenTimeout).Do(req)
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
