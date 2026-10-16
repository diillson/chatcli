/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package fallback

import (
	"context"
	"strconv"
	"strings"

	"github.com/diillson/chatcli/pkg/pulse"
)

// pulseFallbackName is the dashboard node every chain call reports under.
const pulseFallbackName = "fallback"

// chainTrace reports one chain call on the live dashboard: a pattern node
// the providers' own requests hang from, an update per failed attempt
// (provider, attempt, error class) and, at the end, who served the call,
// how many attempts it took and how many providers were skipped while
// cooling down. Error text never travels. A nil trace does nothing.
type chainTrace struct {
	span     *pulse.Span
	attempts int
	skipped  int
}

func (c *Chain) trace(ctx context.Context, op string) (context.Context, *chainTrace) {
	ctx, span := pulse.BeginCtx(ctx, pulse.KindPattern, pulseFallbackName, "")
	if span == nil {
		return ctx, nil
	}
	span.With("op", op).With("entries", strconv.Itoa(len(c.entries)))
	return ctx, &chainTrace{span: span}
}

func (t *chainTrace) skip() {
	if t != nil {
		t.skipped++
	}
}

func (t *chainTrace) fail(provider string, attempt int, class ErrorClass) {
	if t == nil {
		return
	}
	t.attempts++
	t.span.Update(map[string]string{
		"provider":    strings.ToLower(provider),
		"attempt":     strconv.Itoa(attempt + 1),
		"error_class": class.String(),
	})
}

func (t *chainTrace) served(entry FallbackEntry) {
	if t == nil {
		return
	}
	t.attempts++
	t.span.With("served_by", strings.TrimPrefix(strings.ToLower(entry.Provider)+":"+entry.Model, ":"))
}

func (t *chainTrace) end(err error) {
	if t == nil {
		return
	}
	t.span.With("attempts", strconv.Itoa(t.attempts)).With("skipped", strconv.Itoa(t.skipped)).EndErr(err)
}
