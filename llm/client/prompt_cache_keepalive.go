/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Keeping a prompt-cache entry warm without the hour-long ttl.
 *
 * A cache read refreshes the entry's timer at no extra charge on either
 * ttl, so re-sending the previous request with max_tokens: 0 shortly
 * before the 5-minute entry would expire keeps it alive for the price of
 * one cache read and no output. On a model whose reads are a small
 * fraction of the input price that beats paying 2x on every write for
 * the hour-long entry unless pauses regularly approach an hour. The
 * adapter that can do it implements PromptCacheKeepAliver; the surface
 * decides when it is worth it (cli's keep-alive scheduler).
 */
package client

import (
	"context"
	"errors"

	"github.com/diillson/chatcli/models"
)

// PromptCacheKeepAliver is the optional side of an LLM client that can
// refresh the provider's cache entry for its last request: it re-sends
// the same prefix asking for no output and returns the usage the provider
// reported (a cache read, no output tokens), so the caller can book the
// cost. ErrPromptCacheKeepAliveUnsupported means the client, or its
// current auth mode, cannot do it; the caller stops asking.
type PromptCacheKeepAliver interface {
	KeepPromptCacheWarm(ctx context.Context) (*models.UsageInfo, error)
}

// ErrPromptCacheKeepAliveUnsupported reports a client that cannot refresh
// a cache entry: no request has been sent yet, or the wire does not take
// a no-output request.
var ErrPromptCacheKeepAliveUnsupported = errors.New("prompt cache keep-alive unsupported")

// KeepPromptCacheWarm forwards to the inner client when it can refresh
// its cache entry; the refresh is not a turn, so no request metric is
// recorded for it.
func (c *InstrumentedClient) KeepPromptCacheWarm(ctx context.Context) (*models.UsageInfo, error) {
	if c == nil || c.inner == nil {
		return nil, ErrPromptCacheKeepAliveUnsupported
	}
	ka, ok := c.inner.(PromptCacheKeepAliver)
	if !ok {
		return nil, ErrPromptCacheKeepAliveUnsupported
	}
	return ka.KeepPromptCacheWarm(ctx)
}
