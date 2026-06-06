/*
 * ChatCLI - Null TTS provider.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Returned by the factory when no TTS backend is configured. Synthesize always
 * errors so callers can detect that voice output is unavailable and tell the
 * user how to enable it, instead of silently producing nothing.
 */
package tts

import (
	"context"
	"errors"
)

// ErrDisabled is returned by the null provider. Callers compare against it
// (errors.Is) to surface a configuration hint rather than a generic failure.
var ErrDisabled = errors.New("tts: no backend configured — set CHATCLI_TTS_PROVIDER=embedded (keyless, any OS), CHATCLI_TTS_CMD (local), CHATCLI_TTS_URL (self-hosted), or install a local TTS CLI")

// Null is the disabled provider.
type Null struct{}

// NewNull returns the no-op provider.
func NewNull() *Null { return &Null{} }

// Name returns "null".
func (*Null) Name() string { return "null" }

// Synthesize always returns ErrDisabled.
func (*Null) Synthesize(_ context.Context, _, _, _ string) (Audio, error) {
	return Audio{}, ErrDisabled
}
