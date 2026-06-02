/*
 * ChatCLI - Speech-to-text (transcription) provider abstraction.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Transcription is a secondary capability — like llm/embedding — kept
 * separate from the chat LLM client. Audio is converted to text BEFORE the
 * normal text pipeline, so every one of the supported chat providers works
 * unchanged (Anthropic, Gemini, etc. never see audio).
 *
 * Providers wired here:
 *   - openai-compatible : one implementation for self-hosted whisper servers
 *     (whisper.cpp / faster-whisper / Speaches), OpenAI Whisper and Groq —
 *     they all speak POST {base}/audio/transcriptions (multipart).
 *   - null              : default no-op when nothing is configured.
 */
package transcription

import "context"

// Provider turns an audio clip into text. Implementations must be safe for
// concurrent use. mimeType/filename/language are best-effort hints (any may
// be ""); the provider derives sensible defaults.
type Provider interface {
	// Name identifies the provider in logs and /config output.
	Name() string
	// Transcribe returns the recognized text for the audio bytes.
	Transcribe(ctx context.Context, audio []byte, mimeType, filename, language string) (string, error)
}

// IsNull reports whether p is the disabled (no-op) provider — callers use it
// to decide whether voice input is available without invoking it.
func IsNull(p Provider) bool {
	if p == nil {
		return true
	}
	_, ok := p.(*Null)
	return ok
}
