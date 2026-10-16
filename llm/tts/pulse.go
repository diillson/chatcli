/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Speech synthesis on the live dashboard. Every surface that speaks (the
 * @speak tool, gateway voice replies, the web UI) goes through
 * SynthesizeTraced, which puts the call on the bus as one node under
 * whatever started it: backend, model, engine, how much text went in and
 * how much audio came out. Never the text, never the audio, never a voice.
 */
package tts

import (
	"context"
	"strconv"

	"github.com/diillson/chatcli/llm/internal/mediapulse"
	"github.com/diillson/chatcli/pkg/pulse"
)

// pulseSynthesize is the dashboard node name of a synthesis.
const pulseSynthesize = "tts:synthesize"

// SynthesizeTraced runs p.Synthesize as a node on the live dashboard.
func SynthesizeTraced(ctx context.Context, p Provider, text, voice, format string) (Audio, error) {
	ctx, span := beginSynthSpan(ctx, p, len(text))
	audio, err := p.Synthesize(ctx, text, voice, format)
	if span != nil {
		if audio.Ext != "" {
			span.With("format", mediapulse.ProviderLabel(audio.Ext))
		}
		span.With("bytes_out", strconv.Itoa(len(audio.Data))).EndErr(err)
	}
	return audio, err
}

// beginSynthSpan opens the node of one synthesis under the innermost
// parent in ctx.
func beginSynthSpan(ctx context.Context, p Provider, chars int) (context.Context, *pulse.Span) {
	if !pulse.Enabled() || p == nil {
		return ctx, nil
	}
	label := mediapulse.ProviderLabel(p.Name())
	ctx, span := pulse.BeginCtx(ctx, pulse.KindTool, pulseSynthesize, "")
	span.With("provider", label).
		With("model", synthModel(p)).
		With("engine", mediapulse.Engine(label)).
		With("chars_in", strconv.Itoa(chars))
	return ctx, span
}

// synthModel names the model a backend speaks with, "" when it has no
// model notion (a local command).
func synthModel(p Provider) string {
	switch b := p.(type) {
	case *OpenAICompatible:
		return b.model
	case *Google:
		return b.model
	case *embeddedSynth:
		return "kokoro"
	}
	return ""
}
