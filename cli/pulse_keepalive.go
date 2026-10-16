/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Live telemetry for the prompt-cache keep-alive. Each refresh is one
 * background node carrying the route it kept warm, why it fired and what
 * the provider reported for it (the cache read it cost, any write). The
 * refresh request itself hangs from that node as a kind=keepalive LLM
 * request, so a refresh never reads as a turn nobody asked for.
 */
package cli

import (
	"context"
	"errors"
	"strconv"

	llmclient "github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/pkg/pulse"
)

// pulseKeepAliveNode is the dashboard node every refresh aggregates under.
const pulseKeepAliveNode = "cache-keepalive"

// pulseKeepAliveBegin opens the node of one refresh; the returned ctx makes
// the refresh request hang from it.
func pulseKeepAliveBegin(ctx context.Context, provider, model string, reason keepAliveReason, refresh int) (context.Context, *pulse.Span) {
	if !pulse.Enabled() {
		return ctx, nil
	}
	ctx, span := pulse.BeginCtx(ctx, pulse.KindBackground, pulseKeepAliveNode, "")
	span.With("provider", provider).With("model", model).
		With("reason", string(reason)).With("refresh", strconv.Itoa(refresh))
	return ctx, span
}

// pulseKeepAliveEnd closes the node with the usage the refresh reported. A
// client that turned out unable to refresh ends cancelled, not failed: no
// request was sent.
func pulseKeepAliveEnd(span *pulse.Span, usage *models.UsageInfo, err error) {
	if span == nil {
		return
	}
	if errors.Is(err, llmclient.ErrPromptCacheKeepAliveUnsupported) {
		span.Outcome(pulse.StatusCancelled, "unsupported")
		return
	}
	if usage != nil {
		span.With("cache_read", strconv.Itoa(usage.CacheReadInputTokens)).
			With("cache_write", strconv.Itoa(usage.CacheCreationInputTokens))
	}
	span.EndErr(err)
}
