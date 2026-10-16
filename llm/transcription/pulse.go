/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Transcription on the live dashboard. Every surface that transcribes
 * (gateway voice notes, the web UI microphone) goes through
 * TranscribeTraced, which puts the call on the bus as one node under
 * whatever started it: backend, model, engine, how much audio went in and
 * how much text came out. Never the audio, never the transcript, never a
 * file name.
 */
package transcription

import (
	"context"
	"strconv"
	"strings"

	"github.com/diillson/chatcli/llm/internal/mediapulse"
	"github.com/diillson/chatcli/pkg/pulse"
)

// pulseTranscribe is the dashboard node name of a transcription.
const pulseTranscribe = "stt:transcribe"

// TranscribeTraced runs p.Transcribe as a node on the live dashboard.
func TranscribeTraced(ctx context.Context, p Provider, audio []byte, mimeType, filename, language string) (string, error) {
	ctx, span := beginTranscribeSpan(ctx, p, len(audio), mimeType)
	text, err := p.Transcribe(ctx, audio, mimeType, filename, language)
	if span != nil {
		span.With("chars_out", strconv.Itoa(len(text))).EndErr(err)
	}
	return text, err
}

// beginTranscribeSpan opens the node of one transcription under the
// innermost parent in ctx.
func beginTranscribeSpan(ctx context.Context, p Provider, bytesIn int, mimeType string) (context.Context, *pulse.Span) {
	if !pulse.Enabled() || p == nil {
		return ctx, nil
	}
	label := mediapulse.ProviderLabel(p.Name())
	ctx, span := pulse.BeginCtx(ctx, pulse.KindTool, pulseTranscribe, "")
	span.With("provider", label).
		With("model", transcribeModel(p)).
		With("engine", mediapulse.Engine(label)).
		With("bytes_in", strconv.Itoa(bytesIn)).
		With("format", audioFormat(mimeType))
	return ctx, span
}

// transcribeModel names the model a backend transcribes with, "" when it
// has no model notion (a local command).
func transcribeModel(p Provider) string {
	switch b := p.(type) {
	case *OpenAICompatible:
		return b.model
	case *embeddedWhisper:
		return "whisper-" + mediapulse.ProviderLabel(b.size)
	case *localWhisperCpp:
		return "whisper.cpp"
	}
	return ""
}

// audioFormat reduces a MIME type to its subtype word ("audio/ogg;
// codecs=opus" → "ogg"), "" when there is none.
func audioFormat(mimeType string) string {
	_, sub, ok := strings.Cut(mimeType, "/")
	if !ok {
		return ""
	}
	sub, _, _ = strings.Cut(sub, ";")
	if sub = strings.TrimSpace(sub); sub == "" {
		return ""
	}
	return mediapulse.ProviderLabel(sub)
}
