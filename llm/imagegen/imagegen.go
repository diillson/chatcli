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
	"errors"
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

// EditOptions tunes an image-to-image edit request. Zero values mean
// "provider default".
type EditOptions struct {
	Size     string  // output size "WxH" (provider may infer from input)
	N        int     // number of variations (default 1)
	Strength float64 // 0..1 how much to change the input (img2img denoising); 0 = provider default
	Mask     []byte  // optional PNG mask for inpainting (provider-dependent)
}

// Editor is an OPTIONAL capability: turn one or more input images plus a
// prompt into edited output image(s). Backends that support image-to-image
// (SD WebUI img2img, OpenAI /images/edits, etc.) implement it; callers
// type-assert via AsEditor and fail explicitly when it is absent.
type Editor interface {
	Provider
	// Edit returns edited image(s) derived from inputs guided by prompt.
	Edit(ctx context.Context, prompt string, inputs []Image, opts EditOptions) ([]Image, error)
}

// editCapable lets a backend that structurally has an Edit method declare at
// runtime whether the configured endpoint and model actually edit. Editing is
// not one contract: OpenAI Images takes multipart, xAI takes JSON with a data
// URI, Bedrock edits only with some Stability/Amazon models, Z.AI has no edit
// endpoint at all. A backend that does not implement this interface is assumed
// editing-capable (SD WebUI img2img, Google Gemini image).
type editCapable interface {
	supportsEdit() bool
}

// ErrEditUnsupported is returned by Edit on a backend/model that cannot edit,
// so a caller that skipped AsEditor still fails explicitly instead of firing a
// request the endpoint would reject.
var ErrEditUnsupported = errors.New("imagegen: this image backend does not edit images")

// AsEditor returns the Editor view of p when p supports editing. Beyond the
// structural Edit method it honors editCapable, so a generation-only backend
// or model (Z.AI, Bedrock Stable Image Core) is reported as non-editing and
// the caller fails with a clear message instead of a doomed request.
func AsEditor(p Provider) (Editor, bool) {
	if IsNull(p) {
		return nil, false
	}
	e, ok := p.(Editor)
	if !ok {
		return nil, false
	}
	if ec, ok := p.(editCapable); ok && !ec.supportsEdit() {
		return nil, false
	}
	return e, true
}

// modelNamer is implemented by backends that run a specific model.
type modelNamer interface {
	Model() string
}

// ModelOf returns the model p runs, "" when the backend has no model notion
// (SD WebUI runs whatever checkpoint the server loaded).
func ModelOf(p Provider) string {
	if m, ok := p.(modelNamer); ok {
		return m.Model()
	}
	return ""
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
