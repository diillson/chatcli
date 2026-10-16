/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Image generation on the live dashboard. Every surface that generates or
 * edits (the @image tool, the web UI) goes through GenerateTraced or
 * EditTraced, which put the call on the bus as one node under whatever
 * started it: backend, model, how many images went in and came out and
 * their size in bytes. Never the prompt, never an image.
 */
package imagegen

import (
	"context"
	"fmt"
	"strconv"

	"github.com/diillson/chatcli/llm/internal/mediapulse"
	"github.com/diillson/chatcli/pkg/pulse"
)

// Dashboard node names.
const (
	pulseGenerate = "image:generate"
	pulseEdit     = "image:edit"
)

// GenerateTraced runs p.Generate as a node on the live dashboard.
func GenerateTraced(ctx context.Context, p Provider, prompt string, opts Options) ([]Image, error) {
	ctx, span := beginImageSpan(ctx, pulseGenerate, p, opts.Size, opts.N)
	imgs, err := p.Generate(ctx, prompt, opts)
	endImageSpan(span, imgs, err)
	return imgs, err
}

// EditTraced runs e.Edit as a node on the live dashboard.
func EditTraced(ctx context.Context, e Editor, prompt string, inputs []Image, opts EditOptions) ([]Image, error) {
	ctx, span := beginImageSpan(ctx, pulseEdit, e, opts.Size, opts.N)
	span.With("images_in", strconv.Itoa(len(inputs))).
		With("bytes_in", strconv.Itoa(imageBytes(inputs)))
	if len(opts.Mask) > 0 {
		span.With("mask", "true")
	}
	imgs, err := e.Edit(ctx, prompt, inputs, opts)
	endImageSpan(span, imgs, err)
	return imgs, err
}

// beginImageSpan opens the node of one image call under the innermost
// parent in ctx.
func beginImageSpan(ctx context.Context, name string, p Provider, size string, n int) (context.Context, *pulse.Span) {
	if !pulse.Enabled() || p == nil {
		return ctx, nil
	}
	label := mediapulse.ProviderLabel(p.Name())
	ctx, span := pulse.BeginCtx(ctx, pulse.KindTool, name, "")
	span.With("provider", label).
		With("model", ModelOf(p)).
		With("engine", mediapulse.Engine(label)).
		With("size", sizeAttr(size))
	if n > 0 {
		span.With("requested", strconv.Itoa(n))
	}
	return ctx, span
}

// endImageSpan closes the node with what came back.
func endImageSpan(span *pulse.Span, imgs []Image, err error) {
	if span == nil {
		return
	}
	span.With("images", strconv.Itoa(len(imgs))).
		With("bytes_out", strconv.Itoa(imageBytes(imgs))).
		EndErr(err)
}

// imageBytes sums the payload of imgs.
func imageBytes(imgs []Image) int {
	n := 0
	for _, im := range imgs {
		n += len(im.Data)
	}
	return n
}

// sizeAttr renders a requested "WxH" size as numbers only, "" when it is
// not one, so free text given as a size never reaches the bus.
func sizeAttr(size string) string {
	var w, h int
	if _, err := fmt.Sscanf(size, "%dx%d", &w, &h); err != nil || w <= 0 || h <= 0 {
		return ""
	}
	return strconv.Itoa(w) + "x" + strconv.Itoa(h)
}
