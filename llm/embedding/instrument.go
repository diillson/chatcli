/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Embedding usage instrumentation.
 *
 * Embedding calls cost money on every hosted provider and were invisible
 * to the cost tracker. Providers do not all report usage, so the
 * instrumented wrapper measures what left the process — texts and
 * characters — and hands it to an observer; the tracker prices it with
 * the provider's list rate per token (chars/4, an estimate marked as such).
 *
 * The same chokepoint puts each call on the live dashboard as one "embed"
 * node under whatever asked for it: provider, model, batch size, vector
 * dimension and whether the engine runs locally. Never the texts.
 */
package embedding

import (
	"context"
	"strconv"
	"strings"
	"sync"

	"github.com/diillson/chatcli/pkg/pulse"
)

// UsageObserver receives one record per Embed call.
type UsageObserver func(provider string, texts, chars int, err error)

var (
	usageMu       sync.RWMutex
	usageObserver UsageObserver
)

// SetUsageObserver installs the process-wide observer (nil detaches).
func SetUsageObserver(fn UsageObserver) {
	usageMu.Lock()
	usageObserver = fn
	usageMu.Unlock()
}

// Instrumented wraps a Provider so every Embed reports to the observer.
type Instrumented struct {
	Provider
}

// Instrument wraps p (idempotent; the null provider is returned as is).
func Instrument(p Provider) Provider {
	if p == nil || IsNull(p) {
		return p
	}
	if _, already := p.(*Instrumented); already {
		return p
	}
	return &Instrumented{Provider: p}
}

// Embed implements Provider.
func (i *Instrumented) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	ctx, span := beginEmbedSpan(ctx, i.Name(), len(texts))
	out, err := i.Provider.Embed(ctx, texts)
	endEmbedSpan(span, out, err)
	usageMu.RLock()
	fn := usageObserver
	usageMu.RUnlock()
	if fn != nil {
		chars := 0
		for _, t := range texts {
			chars += len(t)
		}
		fn(i.Name(), len(texts), chars, err)
	}
	return out, err
}

// beginEmbedSpan opens the dashboard node of one Embed call. name is the
// provider's "provider:model".
func beginEmbedSpan(ctx context.Context, name string, texts int) (context.Context, *pulse.Span) {
	if !pulse.Enabled() {
		return ctx, nil
	}
	provider, model, _ := strings.Cut(name, ":")
	engine := "remote"
	if provider == "ollama" {
		engine = "local"
	}
	ctx, span := pulse.BeginCtx(ctx, pulse.KindTool, "embed", "")
	span.With("provider", provider).With("model", model).With("engine", engine).
		With("texts", strconv.Itoa(texts))
	return ctx, span
}

// endEmbedSpan closes the node with the vectors' count and dimension.
func endEmbedSpan(span *pulse.Span, out [][]float32, err error) {
	if span == nil {
		return
	}
	span.With("vectors", strconv.Itoa(len(out)))
	if len(out) > 0 {
		span.With("dims", strconv.Itoa(len(out[0])))
	}
	span.EndErr(err)
}

// Unwrap returns the underlying provider.
func (i *Instrumented) Unwrap() Provider { return i.Provider }

// PricePerMillionTokens is the list input price (USD per 1M tokens) of an
// embedding model, 0 for local/unknown ones. Names are "provider:model".
func PricePerMillionTokens(name string) float64 {
	provider, model, _ := strings.Cut(strings.ToLower(name), ":")
	switch provider {
	case "openai":
		switch {
		case strings.Contains(model, "3-large"):
			return 0.13
		case strings.Contains(model, "ada"):
			return 0.10
		default:
			return 0.02
		}
	case "voyage":
		if strings.Contains(model, "large") {
			return 0.18
		}
		return 0.06
	case "google", "gemini":
		return 0.15
	case "bedrock":
		switch {
		case strings.Contains(model, "cohere"):
			return 0.10
		case strings.Contains(model, "nova"):
			return 0.06
		default:
			return 0.02 // Titan v1/v2
		}
	}
	return 0 // ollama, null, unknown
}
