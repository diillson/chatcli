/*
 * ChatCLI - MiniMax H3 Image generation.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * MiniMax-H3-Image and MiniMax-H3-Image-Highspeed (Nov 10 2026) are served
 * by POST /v1/images/generations, a different endpoint and body from
 * Image-01's /v1/image_generation: explicit WIDTHxHEIGHT sizes, images
 * under data[].b64_json, a per-image error object and a top-level error
 * envelope ({"type":"error","error":{type,message,http_code}}). The endpoint
 * refuses any other model id, and Image-01's refuses these.
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
)

const (
	minimaxH3ImagePath = "/images/generations"
	// The server may hold a request for up to 600 seconds.
	minimaxH3Timeout = 600 * time.Second
	// Explicit sizes must cover 655,360 to 20,000,000 pixels, with an
	// aspect ratio between 1:3 and 3:1; they are never scaled or rounded.
	minimaxH3MinArea = 655360
	minimaxH3MaxArea = 20000000
	// A b64_json response is capped at 20,000,000 output pixels (n x width
	// x height) for PNG and 80,000,000 for JPEG.
	minimaxH3PNGPixelCap = 20000000
)

// isMiniMaxH3Image reports whether model is one of the H3 image models.
func isMiniMaxH3Image(model string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(model)), "minimax-h3-image")
}

// minimaxH3ModelID returns the id as the endpoint spells it. The match is
// exact and case-sensitive there ("minimax-h3-image" is an unknown model),
// while the factory routes on a lower-cased name.
func minimaxH3ModelID(model string) string {
	if strings.HasSuffix(strings.ToLower(strings.TrimSpace(model)), "-highspeed") {
		return "MiniMax-H3-Image-Highspeed"
	}
	return "MiniMax-H3-Image"
}

// minimaxH3SizeFits reports whether WxH is a size the endpoint accepts as
// given.
func minimaxH3SizeFits(w, h int) bool {
	area := w * h
	if area < minimaxH3MinArea || area > minimaxH3MaxArea {
		return false
	}
	return w <= 3*h && h <= 3*w
}

// minimaxH3Payload builds the request body. A size the endpoint accepts
// travels as is; one outside its bounds (512x512, a 4:1 banner) falls back
// to the nearest supported aspect ratio at the 1K area, since the endpoint
// refuses the request rather than scaling. The default area is 4K, the
// most expensive: it is never left implicit.
func minimaxH3Payload(model, prompt string, opts Options) (map[string]interface{}, string) {
	n := opts.N
	if n <= 0 {
		n = 1
	}
	payload := map[string]interface{}{
		"model":           minimaxH3ModelID(model),
		"prompt":          prompt,
		"n":               n,
		"response_format": "b64_json",
	}
	w, h := parseSize(opts.Size)
	pixels := n * 1024 * 1024
	if minimaxH3SizeFits(w, h) {
		payload["size"] = fmt.Sprintf("%dx%d", w, h)
		pixels = n * w * h
	} else {
		payload["aspect_ratio"] = minimaxAspect(w, h)
		payload["area"] = "1K"
	}
	format := "png"
	if pixels > minimaxH3PNGPixelCap {
		format = "jpeg"
	}
	payload["output_format"] = format
	return payload, format
}

// minimaxH3Error reads the endpoint's error envelope; "" when body is not
// one.
func minimaxH3Error(body []byte) string {
	var env struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &env) != nil || env.Error.Message == "" {
		return ""
	}
	if env.Error.Type == "" {
		return env.Error.Message
	}
	return env.Error.Type + ": " + env.Error.Message
}

// generateH3 posts the prompt to the H3 endpoint and decodes the images.
// The request is sent once: the endpoint is not idempotent — it finishes
// and bills a generation even when the client goes away, so a retry would
// bill twice.
func (m *MiniMax) generateH3(ctx context.Context, prompt string, opts Options) ([]Image, error) {
	payload, format := minimaxH3Payload(m.model, prompt, opts)
	body, _ := json.Marshal(payload)

	ctx, cancel := context.WithTimeout(ctx, minimaxH3Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.baseURL+minimaxH3ImagePath, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+m.apiKey)

	resp, err := m.h3Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("imagegen: MiniMax request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 4*maxErrBody))
		if msg := minimaxH3Error(snippet); msg != "" {
			return nil, fmt.Errorf("imagegen: MiniMax returned %d: %s", resp.StatusCode, msg)
		}
		if len(snippet) > maxErrBody {
			snippet = snippet[:maxErrBody]
		}
		return nil, fmt.Errorf("imagegen: MiniMax returned %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
	}

	var out struct {
		Data []struct {
			B64JSON string `json:"b64_json"`
			Error   *struct {
				Code    json.Number `json:"code"`
				Message string      `json:"message"`
			} `json:"error"`
		} `json:"data"`
		OutputFormat string `json:"output_format"`
	}
	dec := json.NewDecoder(resp.Body)
	dec.UseNumber()
	if err := dec.Decode(&out); err != nil {
		return nil, fmt.Errorf("imagegen: MiniMax decode: %w", err)
	}
	if out.OutputFormat != "" {
		format = out.OutputFormat
	}
	mime, ext := minimaxH3Format(format)

	// A 200 means at least one image succeeded; the others carry an error
	// in place of the image.
	images := make([]Image, 0, len(out.Data))
	var firstErr string
	for _, item := range out.Data {
		if item.Error != nil {
			if firstErr == "" {
				firstErr = fmt.Sprintf("%s (code %s)", item.Error.Message, item.Error.Code)
			}
			continue
		}
		data, derr := base64.StdEncoding.DecodeString(item.B64JSON)
		if derr != nil || len(data) == 0 {
			continue
		}
		images = append(images, Image{Data: data, Mime: mime, Ext: ext})
	}
	if len(images) == 0 {
		if firstErr != "" {
			return nil, fmt.Errorf("imagegen: MiniMax: %s", firstErr)
		}
		return nil, fmt.Errorf("imagegen: MiniMax returned no decodable images")
	}
	return images, nil
}

// minimaxH3Format maps the endpoint's output_format to a MIME type and a
// file extension.
func minimaxH3Format(format string) (mime, ext string) {
	switch strings.ToLower(format) {
	case "jpeg", "jpg":
		return "image/jpeg", "jpg"
	case "webp":
		return "image/webp", "webp"
	}
	return "image/png", "png"
}
