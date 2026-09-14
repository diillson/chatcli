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
	"encoding/json"
	"errors"
	"fmt"
	"sync"

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

// LastRequestKeeper remembers the body of an adapter's last request so a
// keep-alive can re-send the same prefix. Adapters embed one. The body is
// held as a string on purpose: the exported client structs promise to
// stay comparable, and a byte slice would break that.
type LastRequestKeeper struct {
	mu   sync.Mutex
	body string
}

// Remember stores the body of a request that reached the provider.
func (k *LastRequestKeeper) Remember(body []byte) {
	if k == nil {
		return
	}
	k.mu.Lock()
	k.body = string(body)
	k.mu.Unlock()
}

// Take returns the remembered body, ok=false when nothing was sent yet.
func (k *LastRequestKeeper) Take() ([]byte, bool) {
	if k == nil {
		return nil, false
	}
	k.mu.Lock()
	body := k.body
	k.mu.Unlock()
	if body == "" {
		return nil, false
	}
	return []byte(body), true
}

// KeepAliveRequestBody derives the no-output request from a remembered
// body, for every JSON wire ChatCLI speaks: the output cap is lowered to
// minOutput under whichever name the wire uses (max_tokens on the
// Anthropic and Chat Completions shapes, max_completion_tokens on the
// newer OpenAI models), streaming is removed because a no-output request
// does not stream, and the task budget is removed because its beta
// header is bound to the turn's context. Everything else travels as it
// was: thinking, effort and the messages are part of the cache key, and a
// refresh that changed them would write a new entry instead of
// refreshing this one.
func KeepAliveRequestBody(last []byte, minOutput int) ([]byte, error) {
	var req map[string]interface{}
	if err := json.Unmarshal(last, &req); err != nil {
		return nil, fmt.Errorf("keep-alive body: %w", err)
	}
	if minOutput < 0 {
		minOutput = 0
	}
	for _, key := range []string{"max_tokens", "max_completion_tokens"} {
		if _, ok := req[key]; ok {
			req[key] = minOutput
		}
	}
	delete(req, "stream")
	delete(req, "stream_options")
	if cfg, ok := req["output_config"].(map[string]interface{}); ok {
		delete(cfg, "task_budget")
		if len(cfg) == 0 {
			delete(req, "output_config")
		}
	}
	return json.Marshal(req)
}
