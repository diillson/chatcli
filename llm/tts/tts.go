/*
 * ChatCLI - Text-to-speech (TTS) provider abstraction.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * TTS is a secondary capability — like llm/transcription and llm/embedding —
 * kept separate from the chat LLM client. It turns a final text reply into
 * audio AFTER the normal text pipeline, so every chat provider works unchanged
 * (the models never produce audio themselves).
 *
 * Providers wired here:
 *   - command          : a local CLI (macOS `say`, espeak-ng, piper). Keyless.
 *   - openai-compatible : self-hosted or OpenAI /audio/speech endpoints.
 *   - null             : default no-op when nothing is configured.
 */
package tts

import "context"

// Audio is a synthesized clip plus its MIME type and a file extension hint.
type Audio struct {
	Data []byte
	Mime string // e.g. "audio/mpeg", "audio/ogg", "audio/wav", "audio/aiff"
	Ext  string // e.g. "mp3", "ogg", "wav", "aiff" (no dot)
}

// Provider turns text into speech. Implementations must be safe for concurrent
// use. voice/format are best-effort hints (either may be ""); the provider
// derives sensible defaults.
type Provider interface {
	// Name identifies the provider in logs and /config output.
	Name() string
	// Synthesize returns the spoken audio for text.
	Synthesize(ctx context.Context, text, voice, format string) (Audio, error)
}

// IsNull reports whether p is the disabled (no-op) provider — callers use it to
// decide whether voice output is available without invoking it.
func IsNull(p Provider) bool {
	if p == nil {
		return true
	}
	_, ok := p.(*Null)
	return ok
}

// mimeFor maps a format token to (mime, ext). Defaults to mp3.
func mimeFor(format string) (mime, ext string) {
	switch format {
	case "wav":
		return "audio/wav", "wav"
	case "opus", "ogg":
		return "audio/ogg", "ogg"
	case "aac":
		return "audio/aac", "aac"
	case "flac":
		return "audio/flac", "flac"
	case "aiff":
		return "audio/aiff", "aiff"
	default:
		return "audio/mpeg", "mp3"
	}
}
