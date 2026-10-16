/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Live telemetry for the side calls around a turn: the media bridges
 * (images captioned for a model without vision, PDFs read locally, scanned
 * PDFs transcribed by a model that reads them, documentation images
 * described for @docs-flatten), exact token counts, and the eval harness's
 * judge and run record. Each shows as a node under the turn, run or tool
 * that needed it, and the model call it makes hangs from that node. Only
 * counts, sizes and the chosen provider/model travel — never an image, a
 * page, a file name, a prompt or the text either side produced.
 */
package cli

import (
	"context"
	"strconv"

	"github.com/diillson/chatcli/cli/agent/runs"
	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/pkg/pulse"
)

// Dashboard node names of the side calls.
const (
	pulseVisionDescribe = "vision:describe"
	pulsePDFTranscribe  = "pdf:transcribe"
	pulsePDFExtract     = "pdf:extract"
	pulsePDFRoute       = "pdf:route"
	pulseDocsDescribe   = "docs:describe-image"
	pulseTokensCount    = "tokens:count"
	pulseEvalJudge      = "eval:judge"
	pulseEvalRecordName = "eval:record"
)

// beginMediaPattern opens a media bridge (a hybrid strategy that calls a
// second model) under the innermost parent in ctx, falling back to the
// agent run ctx belongs to.
func beginMediaPattern(ctx context.Context, name string) (context.Context, *pulse.Span) {
	return pulse.BeginCtx(ctx, pulse.KindPattern, name, runs.FromContext(ctx).ID())
}

// beginMediaTool opens a local media step (no model involved) or a single
// model call made on a tool's behalf, under the innermost parent in ctx.
func beginMediaTool(ctx context.Context, name string) (context.Context, *pulse.Span) {
	return pulse.BeginCtx(ctx, pulse.KindTool, name, runs.FromContext(ctx).ID())
}

// imagesBytes sums the payload of images.
func imagesBytes(images []models.ImageContent) int {
	n := 0
	for _, im := range images {
		n += len(im.Data)
	}
	return n
}

// pulseBool renders a flag attribute ("" for false, so it is omitted).
func pulseBool(v bool) string {
	if v {
		return "true"
	}
	return ""
}

// pulsePDFRouteDecision reports how a turn's PDFs reach the model: natively,
// as extracted text, or not at all.
func pulsePDFRouteDecision(ctx context.Context, route string, docs []models.DocumentContent) {
	if !pulse.Enabled() {
		return
	}
	pages := 0
	for _, d := range docs {
		pages += d.Pages
	}
	parent := pulse.ParentFrom(ctx)
	if parent == "" {
		parent = runs.FromContext(ctx).ID()
	}
	pulse.Point(pulse.KindPattern, pulsePDFRoute, parent, pulse.StatusOK, map[string]string{
		"route":     route,
		"documents": strconv.Itoa(len(docs)),
		"pages":     strconv.Itoa(pages),
	})
}
