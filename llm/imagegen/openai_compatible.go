/*
 * ChatCLI - OpenAI-compatible image generation provider.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * POST {base}/images/generations with {model, prompt, n, size,
 * response_format:"b64_json"} returning base64 PNGs. Serves OpenAI and
 * compatible self-hosted servers (LocalAI, etc.).
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
	"strings"
	"time"

	"github.com/diillson/chatcli/utils"
	"go.uber.org/zap"
)

const (
	defaultImageModel = "dall-e-3"
	imageGenTimeout   = 180 * time.Second
	imagesPath        = "/images/generations"
	maxErrBody        = 300
)

// OpenAICompatible generates images against an OpenAI-shaped endpoint.
type OpenAICompatible struct {
	baseURL  string
	apiKey   string
	model    string
	label    string
	omitSize bool // some servers (xAI grok-image) reject the "size" field
	client   *http.Client
}

// NewOpenAICompatible builds the provider. baseURL is required; apiKey may be
// empty for keyless self-hosted servers; model falls back to dall-e-3.
func NewOpenAICompatible(baseURL, apiKey, model, label string, logger *zap.Logger) (*OpenAICompatible, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return nil, fmt.Errorf("imagegen: base URL is required")
	}
	if !strings.HasPrefix(baseURL, "http://") && !strings.HasPrefix(baseURL, "https://") {
		return nil, fmt.Errorf("imagegen: base URL must be http(s): %q", baseURL)
	}
	if strings.TrimSpace(model) == "" {
		model = defaultImageModel
	}
	if strings.TrimSpace(label) == "" {
		label = "openai-compatible"
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	return &OpenAICompatible{
		baseURL: baseURL,
		apiKey:  strings.TrimSpace(apiKey),
		model:   model,
		label:   label,
		client:  utils.NewHTTPClient(logger, imageGenTimeout),
	}, nil
}

// Name returns the backend label.
func (o *OpenAICompatible) Name() string { return o.label }

// Generate posts the prompt and decodes the returned base64 images.
func (o *OpenAICompatible) Generate(ctx context.Context, prompt string, opts Options) ([]Image, error) {
	if strings.TrimSpace(prompt) == "" {
		return nil, fmt.Errorf("imagegen: empty prompt")
	}
	n := opts.N
	if n <= 0 {
		n = 1
	}
	size := opts.Size
	if size == "" {
		size = "1024x1024"
	}

	payload := map[string]interface{}{
		"model":           o.model,
		"prompt":          prompt,
		"n":               n,
		"response_format": "b64_json",
	}
	if !o.omitSize {
		payload["size"] = size
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.baseURL+imagesPath, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if o.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+o.apiKey)
	}

	resp, err := o.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("imagegen: request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrBody))
		return nil, fmt.Errorf("imagegen: %s returned %d: %s", o.label, resp.StatusCode, strings.TrimSpace(string(snippet)))
	}

	var out struct {
		Data []struct {
			B64 string `json:"b64_json"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("imagegen: decode response: %w", err)
	}
	if len(out.Data) == 0 {
		return nil, fmt.Errorf("imagegen: %s returned no images", o.label)
	}
	images := make([]Image, 0, len(out.Data))
	for _, d := range out.Data {
		raw, err := base64.StdEncoding.DecodeString(d.B64)
		if err != nil || len(raw) == 0 {
			continue
		}
		images = append(images, Image{Data: raw, Mime: "image/png", Ext: "png"})
	}
	if len(images) == 0 {
		return nil, fmt.Errorf("imagegen: %s returned no decodable images", o.label)
	}
	return images, nil
}
