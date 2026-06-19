/*
 * ChatCLI - Stable Diffusion WebUI (AUTOMATIC1111) image provider.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * POST {base}/sdapi/v1/txt2img with {prompt, steps, width, height,
 * batch_size} returning base64 PNGs in {images:[...]}. This is the keyless,
 * local, self-hosted backend — the preferred option for privacy and cost.
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

	"github.com/diillson/chatcli/utils"
	"go.uber.org/zap"
)

const (
	sdTxt2ImgPath = "/sdapi/v1/txt2img"
	sdImg2ImgPath = "/sdapi/v1/img2img"
)

// Automatic1111 generates images against a local Stable Diffusion WebUI.
type Automatic1111 struct {
	baseURL string
	steps   int
	client  *http.Client
}

// NewAutomatic1111 builds the provider. baseURL defaults to localhost:7860.
func NewAutomatic1111(baseURL string, steps int, logger *zap.Logger) (*Automatic1111, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		baseURL = "http://localhost:7860"
	}
	if !strings.HasPrefix(baseURL, "http://") && !strings.HasPrefix(baseURL, "https://") {
		return nil, fmt.Errorf("imagegen: base URL must be http(s): %q", baseURL)
	}
	if steps <= 0 {
		steps = 25
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Automatic1111{baseURL: baseURL, steps: steps, client: utils.NewHTTPClientH1(logger, imageGenTimeout)}, nil
}

// Name returns "sdwebui".
func (*Automatic1111) Name() string { return "sdwebui" }

// Generate posts the prompt and decodes the returned base64 images.
func (a *Automatic1111) Generate(ctx context.Context, prompt string, opts Options) ([]Image, error) {
	if strings.TrimSpace(prompt) == "" {
		return nil, fmt.Errorf("imagegen: empty prompt")
	}
	w, h := parseSize(opts.Size)
	n := opts.N
	if n <= 0 {
		n = 1
	}
	body, _ := json.Marshal(map[string]interface{}{
		"prompt":     prompt,
		"steps":      a.steps,
		"width":      w,
		"height":     h,
		"batch_size": n,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL+sdTxt2ImgPath, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("imagegen: request failed (is Stable Diffusion WebUI running with --api?): %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrBody))
		return nil, fmt.Errorf("imagegen: sdwebui returned %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
	}

	var out struct {
		Images []string `json:"images"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("imagegen: decode response: %w", err)
	}
	return decodeSDImages(out.Images)
}

// Edit runs Stable Diffusion img2img: the input image(s) seed the diffusion
// and the prompt guides the transformation. Strength maps to denoising_strength
// (how far from the original the result may drift).
func (a *Automatic1111) Edit(ctx context.Context, prompt string, inputs []Image, opts EditOptions) ([]Image, error) {
	if strings.TrimSpace(prompt) == "" {
		return nil, fmt.Errorf("imagegen: empty prompt")
	}
	if len(inputs) == 0 || len(inputs[0].Data) == 0 {
		return nil, fmt.Errorf("imagegen: edit requires an input image")
	}
	w, h := parseSize(opts.Size)
	n := opts.N
	if n <= 0 {
		n = 1
	}
	denoise := opts.Strength
	if denoise <= 0 || denoise > 1 {
		denoise = 0.6 // a sensible default: visible change while preserving structure
	}
	payload := map[string]interface{}{
		"prompt":             prompt,
		"init_images":        []string{base64.StdEncoding.EncodeToString(inputs[0].Data)},
		"denoising_strength": denoise,
		"steps":              a.steps,
		"width":              w,
		"height":             h,
		"batch_size":         n,
	}
	if len(opts.Mask) > 0 {
		payload["mask"] = base64.StdEncoding.EncodeToString(opts.Mask)
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL+sdImg2ImgPath, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("imagegen: request failed (is Stable Diffusion WebUI running with --api?): %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrBody))
		return nil, fmt.Errorf("imagegen: sdwebui img2img returned %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
	}
	var out struct {
		Images []string `json:"images"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("imagegen: decode response: %w", err)
	}
	return decodeSDImages(out.Images)
}

// decodeSDImages decodes the base64 image list returned by both the txt2img
// and img2img endpoints, stripping any data-URI prefix.
func decodeSDImages(b64s []string) ([]Image, error) {
	images := make([]Image, 0, len(b64s))
	for _, b64 := range b64s {
		if i := strings.Index(b64, ","); strings.HasPrefix(b64, "data:") && i >= 0 {
			b64 = b64[i+1:]
		}
		raw, err := base64.StdEncoding.DecodeString(b64)
		if err != nil || len(raw) == 0 {
			continue
		}
		images = append(images, Image{Data: raw, Mime: "image/png", Ext: "png"})
	}
	if len(images) == 0 {
		return nil, fmt.Errorf("imagegen: sdwebui returned no decodable images")
	}
	return images, nil
}
