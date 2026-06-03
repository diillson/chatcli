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
func KnownModels() []ModelInfo {
	return []ModelInfo{
		{Name: "gpt-image-1", Provider: "openai", API: "images", Note: "Current OpenAI image model (Images API)."},
		{Name: "dall-e-3", Provider: "openai", API: "images", Note: "Legacy; not on all accounts."},
		{Name: "dall-e-2", Provider: "openai", API: "images", Note: "Legacy."},
		{Name: "gpt-5.5", Provider: "openai", API: "responses", Note: "Chat model w/ image_generation tool (Responses API)."},
		{Name: "gpt-5", Provider: "openai", API: "responses", Note: "Responses API image_generation."},
		{Name: "gpt-4.1", Provider: "openai", API: "responses", Note: "Responses API image_generation."},
		{Name: "imagen-3.0-generate-002", Provider: "google", API: "native", Note: "Google Imagen (predict)."},
		{Name: "grok-2-image", Provider: "xai", API: "native", Note: "xAI grok image (OpenAI-shaped, no size)."},
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
