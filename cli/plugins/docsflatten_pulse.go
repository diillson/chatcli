/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * @docs-flatten on the live dashboard. A flatten can crawl hundreds of
 * pages and describe dozens of images with a vision model; one node under
 * the tool call that ran it says how much it read and produced, and every
 * image description it asks for hangs from it. Only counts and the fixed
 * option words travel — never a path, a URL, a repository or any content.
 */
package plugins

import (
	"context"
	"sort"
	"strconv"
	"strings"

	"github.com/diillson/chatcli/pkg/pulse"
)

// pulseDocsFlatten is the dashboard node name of one flatten run.
const pulseDocsFlatten = "docs:flatten"

// docsFlattenStats is what one run did, filled in as it goes.
type docsFlattenStats struct {
	files  int // local/git source: files read
	pages  int // url source: pages fetched
	chunks int
	capped bool
	images *docsImageReport
}

// beginDocsFlattenSpan opens the node of a run with its source kind and
// options, all from fixed sets validated by the argument parser.
func beginDocsFlattenSpan(ctx context.Context, cfg docsFlattenArgs) (context.Context, *pulse.Span) {
	if !pulse.Enabled() {
		return ctx, nil
	}
	source := "local"
	switch {
	case cfg.URL != "":
		source = "url"
	case cfg.Repo != "":
		source = "git"
	}
	ctx, span := pulse.BeginCtx(ctx, pulse.KindTool, pulseDocsFlatten, "")
	span.With("source", source).
		With("kind", cfg.Kind).
		With("format", cfg.Format).
		With("images_mode", cfg.Images)
	return ctx, span
}

// end closes the node with what the run produced.
func (st *docsFlattenStats) end(span *pulse.Span, err error) {
	if span == nil {
		return
	}
	if st.files > 0 {
		span.With("files", strconv.Itoa(st.files))
	}
	if st.pages > 0 {
		span.With("pages", strconv.Itoa(st.pages))
	}
	span.With("chunks", strconv.Itoa(st.chunks))
	if st.capped {
		span.With("capped", "true")
	}
	if r := st.images; r != nil && r.Found > 0 {
		span.With("images", strconv.Itoa(r.Found)).
			With("images_described", strconv.Itoa(r.Described)).
			With("images_cached", strconv.Itoa(r.Cached)).
			With("describer", describerModels(r.Models))
		if r.NeedConfirm > 0 {
			span.With("needs_confirm", strconv.Itoa(r.NeedConfirm))
		}
	}
	span.EndErr(err)
}

// describerModels names the provider/model pairs that described images,
// sorted and comma-joined ("" when none did).
func describerModels(models map[string]int) string {
	names := make([]string, 0, len(models))
	for m := range models {
		if m != "" {
			names = append(names, m)
		}
	}
	sort.Strings(names)
	return strings.Join(names, ",")
}
