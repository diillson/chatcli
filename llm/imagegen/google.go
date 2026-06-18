/*
 * ChatCLI - Google Imagen image generation provider.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Google's image API does NOT speak the OpenAI shape, so it needs its own
 * backend: POST {base}/v1beta/models/{model}:predict?key=KEY with
 * {instances:[{prompt}], parameters:{sampleCount}} returning
 * {predictions:[{bytesBase64Encoded, mimeType}]}. This is what lets @image use
 * Gemini/Imagen instead of being limited to OpenAI-shaped backends.
 */
package imagegen

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/diillson/chatcli/utils"
	"go.uber.org/zap"
)

const (
	googleImageBase    = "https://generativelanguage.googleapis.com"
	defaultImagenModel = "imagen-3.0-generate-002"
	// defaultGeminiImageModel is a Gemini image model ("Nano Banana") that
	// edits via :generateContent with an inline input image. Imagen's :predict
	// endpoint is generation-only, so editing routes through this model.
	defaultGeminiImageModel = "gemini-2.5-flash-image"
)

// Google generates images via the Imagen predict endpoint.
type Google struct {
	baseURL string
	apiKey  string
	model   string
	client  *http.Client
}

// NewGoogle builds the provider. apiKey is required (the user's own key).
func NewGoogle(apiKey, model string, logger *zap.Logger) (*Google, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, fmt.Errorf("imagegen: Google API key is required")
	}
	if strings.TrimSpace(model) == "" {
		model = defaultImagenModel
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Google{baseURL: googleImageBase, apiKey: strings.TrimSpace(apiKey), model: model, client: utils.NewHTTPClient(logger, imageGenTimeout)}, nil
}

// Name returns "google".
func (*Google) Name() string { return "google" }

// Generate posts the prompt and decodes the returned base64 images.
func (g *Google) Generate(ctx context.Context, prompt string, opts Options) ([]Image, error) {
	if strings.TrimSpace(prompt) == "" {
		return nil, fmt.Errorf("imagegen: empty prompt")
	}
	n := opts.N
	if n <= 0 {
		n = 1
	}
	body, _ := json.Marshal(map[string]interface{}{
		"instances":  []map[string]string{{"prompt": prompt}},
		"parameters": map[string]interface{}{"sampleCount": n},
	})
	endpoint := fmt.Sprintf("%s/v1beta/models/%s:predict?key=%s", g.baseURL, g.model, g.apiKey)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := g.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("imagegen: request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrBody))
		return nil, fmt.Errorf("imagegen: google returned %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
	}

	var out struct {
		Predictions []struct {
			Bytes string `json:"bytesBase64Encoded"`
			Mime  string `json:"mimeType"`
		} `json:"predictions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("imagegen: decode response: %w", err)
	}
	images := make([]Image, 0, len(out.Predictions))
	for _, p := range out.Predictions {
		raw, err := base64.StdEncoding.DecodeString(p.Bytes)
		if err != nil || len(raw) == 0 {
			continue
		}
		mime := p.Mime
		if mime == "" {
			mime = "image/png"
		}
		ext := "png"
		if strings.Contains(mime, "jpeg") {
			ext = "jpg"
		}
		images = append(images, Image{Data: raw, Mime: mime, Ext: ext})
	}
	if len(images) == 0 {
		return nil, fmt.Errorf("imagegen: google returned no decodable images")
	}
	return images, nil
}

// Edit transforms the input image(s) guided by prompt via the Gemini image
// model's :generateContent endpoint (the input image rides as inline_data).
// Imagen's :predict endpoint cannot do conversational editing, so when the
// configured model is an Imagen one we route the edit through a Gemini image
// model.
func (g *Google) Edit(ctx context.Context, prompt string, inputs []Image, opts EditOptions) ([]Image, error) {
	if strings.TrimSpace(prompt) == "" {
		return nil, fmt.Errorf("imagegen: empty prompt")
	}
	if len(inputs) == 0 || len(inputs[0].Data) == 0 {
		return nil, fmt.Errorf("imagegen: edit requires an input image")
	}
	model := g.model
	if strings.HasPrefix(strings.ToLower(model), "imagen") {
		model = defaultGeminiImageModel
	}

	parts := []map[string]interface{}{{"text": prompt}}
	for _, in := range inputs {
		if len(in.Data) == 0 {
			continue
		}
		mime := in.Mime
		if mime == "" {
			mime = "image/png"
		}
		parts = append(parts, map[string]interface{}{
			"inline_data": map[string]interface{}{
				"mime_type": mime,
				"data":      base64.StdEncoding.EncodeToString(in.Data),
			},
		})
	}

	body, _ := json.Marshal(map[string]interface{}{
		"contents": []map[string]interface{}{{"role": "user", "parts": parts}},
		"generationConfig": map[string]interface{}{
			"responseModalities": []string{"IMAGE"},
		},
	})
	endpoint := fmt.Sprintf("%s/v1beta/models/%s:generateContent?key=%s", g.baseURL, model, g.apiKey)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := g.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("imagegen: request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrBody))
		return nil, fmt.Errorf("imagegen: google edit returned %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
	}

	var out struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					InlineData struct {
						Data string `json:"data"`
						Mime string `json:"mimeType"`
					} `json:"inlineData"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("imagegen: decode response: %w", err)
	}
	var images []Image
	for _, c := range out.Candidates {
		for _, p := range c.Content.Parts {
			if p.InlineData.Data == "" {
				continue
			}
			raw, err := base64.StdEncoding.DecodeString(p.InlineData.Data)
			if err != nil || len(raw) == 0 {
				continue
			}
			mime := p.InlineData.Mime
			if mime == "" {
				mime = "image/png"
			}
			ext := "png"
			if strings.Contains(mime, "jpeg") {
				ext = "jpg"
			}
			images = append(images, Image{Data: raw, Mime: mime, Ext: ext})
		}
	}
	if len(images) == 0 {
		return nil, fmt.Errorf("imagegen: google edit returned no image (model %q may not support image output)", model)
	}
	return images, nil
}

// googleImageKey returns the user's Google API key from the usual env names.
func googleImageKey() string {
	for _, k := range []string{"GOOGLEAI_API_KEY", "GEMINI_API_KEY", "GOOGLE_API_KEY"} {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return ""
}
