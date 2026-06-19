/*
 * ChatCLI - MiniMax (Hailuo) image generation provider.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * MiniMax Image-01 is NOT OpenAI-shaped: it POSTs to a dedicated
 * /v1/image_generation endpoint with an aspect_ratio (not a WxH size) and
 * returns base64 strings under data.image_base64. Errors arrive in base_resp.
 * Credentials reuse the same MINIMAX_API_KEY as the MiniMax chat provider.
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
	minimaxImageBaseURL      = "https://api.minimax.io/v1"
	minimaxImagePath         = "/image_generation"
	defaultMiniMaxImageModel = "image-01"
)

// MiniMax generates images via the MiniMax Image-01 endpoint.
type MiniMax struct {
	baseURL string
	apiKey  string
	model   string
	client  *http.Client
}

// NewMiniMax builds the provider. apiKey is required (MiniMax is not keyless);
// model falls back to image-01.
func NewMiniMax(baseURL, apiKey, model string, logger *zap.Logger) (*MiniMax, error) {
	if logger == nil {
		logger = zap.NewNop()
	}
	if strings.TrimSpace(apiKey) == "" {
		return nil, fmt.Errorf("imagegen: MiniMax requires MINIMAX_API_KEY")
	}
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		baseURL = minimaxImageBaseURL
	}
	if strings.TrimSpace(model) == "" {
		model = defaultMiniMaxImageModel
	}
	return &MiniMax{
		baseURL: baseURL,
		apiKey:  strings.TrimSpace(apiKey),
		model:   model,
		client:  utils.NewHTTPClientH1(logger, imageGenTimeout),
	}, nil
}

// Name returns "minimax".
func (*MiniMax) Name() string { return "minimax" }

// Generate posts the prompt and decodes the base64 images MiniMax returns.
func (m *MiniMax) Generate(ctx context.Context, prompt string, opts Options) ([]Image, error) {
	if strings.TrimSpace(prompt) == "" {
		return nil, fmt.Errorf("imagegen: empty prompt")
	}
	n := opts.N
	if n <= 0 {
		n = 1
	}
	w, h := parseSize(opts.Size)
	payload := map[string]interface{}{
		"model":           m.model,
		"prompt":          prompt,
		"aspect_ratio":    minimaxAspect(w, h),
		"response_format": "base64",
		"n":               n,
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.baseURL+minimaxImagePath, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+m.apiKey)

	resp, err := m.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("imagegen: MiniMax request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrBody))
		return nil, fmt.Errorf("imagegen: MiniMax returned %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
	}

	var out struct {
		Data struct {
			ImageBase64 []string `json:"image_base64"`
		} `json:"data"`
		BaseResp struct {
			StatusCode int    `json:"status_code"`
			StatusMsg  string `json:"status_msg"`
		} `json:"base_resp"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("imagegen: MiniMax decode: %w", err)
	}
	// MiniMax signals logical errors in base_resp even on HTTP 200.
	if out.BaseResp.StatusCode != 0 {
		return nil, fmt.Errorf("imagegen: MiniMax: %s (code %d)", out.BaseResp.StatusMsg, out.BaseResp.StatusCode)
	}
	images := make([]Image, 0, len(out.Data.ImageBase64))
	for _, b64 := range out.Data.ImageBase64 {
		data, derr := base64.StdEncoding.DecodeString(b64)
		if derr != nil || len(data) == 0 {
			continue
		}
		images = append(images, Image{Data: data, Mime: "image/png", Ext: "png"})
	}
	if len(images) == 0 {
		return nil, fmt.Errorf("imagegen: MiniMax returned no decodable images")
	}
	return images, nil
}

// minimaxAspect maps a width/height to the nearest MiniMax-supported aspect
// ratio (MiniMax takes aspect_ratio, not explicit pixels). The thresholds are
// the midpoints between adjacent supported ratios, so each input snaps to its
// closest neighbor. Supported set and their decimal r=w/h:
//
//	21:9=2.333  16:9=1.778  3:2=1.5  4:3=1.333  1:1=1.0
//	3:4=0.75    2:3=0.667   9:16=0.5625
func minimaxAspect(w, h int) string {
	if w <= 0 || h <= 0 || w == h {
		return "1:1"
	}
	r := float64(w) / float64(h)
	switch {
	case r >= 2.056: // midpoint(16:9, 21:9)
		return "21:9"
	case r >= 1.639: // midpoint(3:2, 16:9)
		return "16:9"
	case r >= 1.417: // midpoint(4:3, 3:2)
		return "3:2"
	case r >= 1.167: // midpoint(1:1, 4:3)
		return "4:3"
	case r <= 0.615: // midpoint(9:16, 2:3)
		return "9:16"
	case r <= 0.708: // midpoint(2:3, 3:4)
		return "2:3"
	case r <= 0.875: // midpoint(3:4, 1:1)
		return "3:4"
	default:
		return "1:1"
	}
}
