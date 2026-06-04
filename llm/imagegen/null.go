/*
 * ChatCLI - Null image generation provider.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Returned by the factory when no image backend is configured. Generate always
 * errors so callers can detect that image generation is unavailable and tell
 * the user how to enable it.
 */
package imagegen

import (
	"context"
	"errors"
)

// ErrDisabled is returned by the null provider.
var ErrDisabled = errors.New("imagegen: no backend configured — set CHATCLI_IMAGE_URL (self-hosted, e.g. Stable Diffusion WebUI) or OPENAI_API_KEY")

// Null is the disabled provider.
type Null struct{}

// NewNull returns the no-op provider.
func NewNull() *Null { return &Null{} }

// Name returns "null".
func (*Null) Name() string { return "null" }

// Generate always returns ErrDisabled.
func (*Null) Generate(_ context.Context, _ string, _ Options) ([]Image, error) {
	return nil, ErrDisabled
}
