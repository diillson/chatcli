/*
 * ChatCLI - Null transcription provider.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Returned by the factory when no transcription backend is configured.
 * Transcribe always errors so callers can detect that voice input is
 * unavailable and tell the user how to enable it, instead of silently
 * dropping the message.
 */
package transcription

import (
	"context"
	"errors"
)

// ErrDisabled is returned by the null provider. Callers compare against it
// (errors.Is) to surface a configuration hint rather than a generic failure.
var ErrDisabled = errors.New("transcription: no backend configured — set CHATCLI_TRANSCRIPTION_PROVIDER=embedded (offline, no key), CHATCLI_TRANSCRIPTION_URL (self-hosted) or a cloud key")

// Null is the disabled provider.
type Null struct{}

// NewNull returns the no-op provider.
func NewNull() *Null { return &Null{} }

// Name returns "null".
func (*Null) Name() string { return "null" }

// Transcribe always returns ErrDisabled.
func (*Null) Transcribe(_ context.Context, _ []byte, _, _, _ string) (string, error) {
	return "", ErrDisabled
}
