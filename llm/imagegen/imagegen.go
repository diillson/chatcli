/*
 * ChatCLI - Image generation provider abstraction.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Image generation is a secondary capability — like llm/tts and
 * llm/transcription — kept separate from the chat LLM client.
 *
 * Providers wired here:
 *   - sdwebui          : a self-hosted AUTOMATIC1111 Stable Diffusion WebUI
 *     (POST /sdapi/v1/txt2img). Keyless, local — the preferred backend.
 *   - openai-compatible : OpenAI /images/generations and compatible servers
 *     (LocalAI, etc.).
 *   - null             : default no-op when nothing is configured.
 */
package imagegen

import (
	"context"
	"fmt"
)

// Image is one generated picture.
type Image struct {
	Data []byte
	Mime string // e.g. "image/png"
	Ext  string // e.g. "png"
}

// Options tunes a generation request. Zero values mean "provider default".
type Options struct {
	Size string // e.g. "1024x1024"
	N    int    // number of images (default 1)
}

// Provider turns a text prompt into one or more images. Implementations must be
// safe for concurrent use.
type Provider interface {
	// Name identifies the provider in logs and /config output.
	Name() string
	// Generate returns the images for prompt.
	Generate(ctx context.Context, prompt string, opts Options) ([]Image, error)
}

// IsNull reports whether p is the disabled (no-op) provider.
func IsNull(p Provider) bool {
	if p == nil {
		return true
	}
	_, ok := p.(*Null)
	return ok
}

// parseSize splits "WxH" into width, height. Defaults to 1024x1024.
func parseSize(size string) (w, h int) {
	var a, b int
	if _, err := fmt.Sscanf(size, "%dx%d", &a, &b); err == nil && a > 0 && b > 0 {
		return a, b
	}
	return 1024, 1024
}
