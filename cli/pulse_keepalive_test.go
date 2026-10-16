/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"context"
	"testing"

	llmclient "github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/pkg/pulse"
	"github.com/diillson/chatcli/pkg/pulse/pulsetest"
)

// loggedWarmClient refreshes through the same request-log chokepoint the
// provider adapters use.
type loggedWarmClient struct {
	llmclient.MockLLMClient
}

func (w *loggedWarmClient) KeepPromptCacheWarm(ctx context.Context) (*models.UsageInfo, error) {
	return llmclient.LogKeepAliveRequest(ctx, nil, "CLAUDEAI", "claude-fable-5-1", 1234, func() (*models.UsageInfo, error) {
		return &models.UsageInfo{IsReal: true, CacheReadInputTokens: 40000, CacheCreationInputTokens: 12}, nil
	})
}

// A refresh is a background node with its route, reason and the cache
// read/write it reported, and the refresh request hangs from it as a
// kind=keepalive LLM request.
func TestPulseKeepAliveRefreshIsANodeWithItsRequest(t *testing.T) {
	rec := pulsetest.Record(t)
	tap := newPulseLLMTap(pulse.Default())
	llmclient.RegisterRequestAuditorKeyed("pulse-keepalive-test", tap.observe)
	llmclient.SetCallerResolver(pulseCallerFrom)
	t.Cleanup(func() {
		llmclient.RegisterRequestAuditorKeyed("pulse-keepalive-test", nil)
		llmclient.SetCallerResolver(nil)
	})

	cli := keepAliveCLI(t)
	cli.Client = &loggedWarmClient{}
	cli.armPromptCacheKeepAlive("CLAUDEAI", "claude-fable-5-1", 0)
	ka := &cli.cacheKeepAlive
	ka.mu.Lock()
	gen := ka.gen
	ka.mu.Unlock()
	if !cli.firePromptCacheKeepAlive(gen) {
		t.Fatal("the refresh must fire")
	}

	node := rec.WaitFor(t, "keep-alive end", pulsetest.Is(pulse.KindBackground, pulseKeepAliveNode, pulse.PhaseEnd))
	want := map[string]string{"provider": "CLAUDEAI", "model": "claude-fable-5-1", "refresh": "1", "cache_read": "40000", "cache_write": "12"}
	for k, v := range want {
		if node.Attrs[k] != v {
			t.Fatalf("attr %s = %q, want %q (%+v)", k, node.Attrs[k], v, node.Attrs)
		}
	}
	if node.Status != pulse.StatusOK || node.Attrs["reason"] == "" {
		t.Fatalf("keep-alive node: %+v", node)
	}
	req := rec.WaitFor(t, "keep-alive request end", func(ev pulse.Event) bool {
		return ev.Kind == pulse.KindLLM && ev.Phase == pulse.PhaseEnd
	})
	if req.Parent != node.ID || req.Attrs["kind"] != llmclient.KeepAliveRequestKind || req.Attrs["cache_read_tokens"] != "40000" {
		t.Fatalf("keep-alive request: %+v (node %s)", req, node.ID)
	}
	sent := rec.WaitFor(t, "keep-alive request start", func(ev pulse.Event) bool { return ev.ID == req.ID && ev.Phase == pulse.PhaseStart })
	if sent.Attrs["kind"] != llmclient.KeepAliveRequestKind || sent.Attrs["payload_bytes"] != "1234" {
		t.Fatalf("keep-alive request start: %+v", sent)
	}
}

// A client that cannot refresh ends its node cancelled: nothing was sent.
func TestPulseKeepAliveUnsupportedEndsCancelled(t *testing.T) {
	rec := pulsetest.Record(t)
	cli := keepAliveCLI(t)
	cli.Client = &warmClient{err: llmclient.ErrPromptCacheKeepAliveUnsupported}
	cli.armPromptCacheKeepAlive("CLAUDEAI", "claude-fable-5-1", 0)
	ka := &cli.cacheKeepAlive
	ka.mu.Lock()
	gen := ka.gen
	ka.mu.Unlock()
	cli.firePromptCacheKeepAlive(gen)
	node := rec.WaitFor(t, "keep-alive end", pulsetest.Is(pulse.KindBackground, pulseKeepAliveNode, pulse.PhaseEnd))
	if node.Status != pulse.StatusCancelled || node.Attrs["state"] != "unsupported" {
		t.Fatalf("unsupported refresh: %+v", node)
	}
}
