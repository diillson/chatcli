/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package provision

import (
	"context"
	"io"
	"strings"
)

// Provision phases reported through a Progress hook.
const (
	PhaseDownload = "download"
	PhaseExtract  = "extract"
)

// Progress is one step of a one-time engine or model provision: bytes
// received for an archive while it downloads, then a single extract step.
// Total is -1 when the server sent no length; Done is -1 for the extract
// step, which has no byte count worth reporting.
type Progress struct {
	Phase string
	File  string
	Done  int64
	Total int64
}

type progressKey struct{}

// WithProgress returns a context whose provisioning reports each step to
// fn. A surface that asked the user before downloading (the web UI) shows
// the bytes as they arrive instead of a silent multi-minute wait. fn runs
// on the downloading goroutine and must not block.
func WithProgress(ctx context.Context, fn func(Progress)) context.Context {
	if fn == nil {
		return ctx
	}
	return context.WithValue(ctx, progressKey{}, fn)
}

// report delivers p to the context's hook, if any.
func report(ctx context.Context, p Progress) {
	if fn, ok := ctx.Value(progressKey{}).(func(Progress)); ok {
		fn(p)
	}
}

// progressBody wraps a download body so every read reports the running
// byte count; without a hook the body is returned untouched.
func progressBody(ctx context.Context, body io.Reader, url string, total int64) io.Reader {
	fn, ok := ctx.Value(progressKey{}).(func(Progress))
	if !ok {
		return body
	}
	if total <= 0 {
		total = -1
	}
	file := archiveName(url)
	fn(Progress{Phase: PhaseDownload, File: file, Total: total})
	return &countingReader{r: body, fn: fn, file: file, total: total}
}

// archiveName is the last path element of a download URL.
func archiveName(url string) string {
	if i := strings.LastIndexByte(url, '/'); i >= 0 {
		return url[i+1:]
	}
	return url
}

type countingReader struct {
	r     io.Reader
	fn    func(Progress)
	file  string
	done  int64
	total int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	if n > 0 {
		c.done += int64(n)
		c.fn(Progress{Phase: PhaseDownload, File: c.file, Done: c.done, Total: c.total})
	}
	return n, err
}
