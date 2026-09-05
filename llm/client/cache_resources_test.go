/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package client

import (
	"context"
	"testing"
	"time"
)

// TestPromptCacheTTL_AutoSettlesOncePerConversation pins what "auto"
// means now. It used to follow the surface hint on every call, which read
// as flexibility and behaved as a leak: the ttl is part of the
// cache_control marker, so its value is prefix bytes, and a session that
// crossed between chat and the agent loop re-marked the same messages and
// threw away the prefix it had just paid for. Whichever surface asks
// first decides for the conversation.
func TestPromptCacheTTL_AutoSettlesOncePerConversation(t *testing.T) {
	t.Setenv(PromptCacheTTLEnv, "auto")
	t.Cleanup(func() { SetPromptCacheTTLHint("5m"); ResetPromptCacheTTL() })

	// A conversation that starts in chat holds the 5-minute default, and
	// entering the agent loop later does not rewrite its prefix.
	ResetPromptCacheTTL()
	SetPromptCacheTTLHint("5m")
	if got := AnthropicCacheTTL(); got != "5m" {
		t.Fatalf("a chat-first conversation settles on 5m, got %s", got)
	}
	SetPromptCacheTTLHint("1h")
	if got := AnthropicCacheTTL(); got != "5m" {
		t.Fatalf("entering the agent loop must not re-mark a settled conversation, got %s", got)
	}

	// A conversation that starts in the agent loop holds the hour, and
	// dropping back to chat does not take it away either.
	ResetPromptCacheTTL()
	SetPromptCacheTTLHint("1h")
	if got := AnthropicCacheTTL(); got != "1h" {
		t.Fatalf("an agent-first conversation settles on 1h, got %s", got)
	}
	if PromptCacheTTLDuration() != time.Hour {
		t.Fatal("duration must follow the settled TTL")
	}
	SetPromptCacheTTLHint("5m")
	if got := AnthropicCacheTTL(); got != "1h" {
		t.Fatalf("leaving the agent loop must not re-mark a settled conversation, got %s", got)
	}

	// A new conversation resolves again: the prefix is gone anyway.
	ResetPromptCacheTTL()
	if got := AnthropicCacheTTL(); got != "5m" {
		t.Fatalf("a restarted conversation resolves fresh, got %s", got)
	}

	// An explicit value ignores the hint and never settles anything.
	t.Setenv(PromptCacheTTLEnv, "5m")
	SetPromptCacheTTLHint("1h")
	if got := AnthropicCacheTTL(); got != "5m" {
		t.Fatalf("explicit 5m must win over the hint, got %s", got)
	}
}

func TestExplicitCacheEnabled_Parsing(t *testing.T) {
	for v, want := range map[string]bool{"": false, "false": false, "0": false, "true": true, "1": true, "ON": true, "yes": true, "maybe": false} {
		t.Setenv(PromptCacheExplicitEnv, v)
		if got := ExplicitCacheEnabled(); got != want {
			t.Fatalf("%q → %v, want %v", v, got, want)
		}
	}
}

func TestCacheReleasers_RunOnceInOrderAndClear(t *testing.T) {
	var order []string
	RegisterCacheReleaser("b", func(context.Context) { order = append(order, "b") })
	RegisterCacheReleaser("a", func(context.Context) { order = append(order, "a") })
	RegisterCacheReleaser("", func(context.Context) { order = append(order, "empty") })
	if n := ReleaseCacheResources(context.Background()); n != 2 {
		t.Fatalf("two releasers expected, got %d", n)
	}
	if len(order) != 2 || order[0] != "a" || order[1] != "b" {
		t.Fatalf("releasers must run in key order, got %v", order)
	}
	if n := ReleaseCacheResources(context.Background()); n != 0 {
		t.Fatalf("registry must be cleared, got %d", n)
	}
}

func TestEmitCacheResource_StampsTime(t *testing.T) {
	var got CacheResourceEvent
	RegisterCacheResourceObserver(func(ev CacheResourceEvent) { got = ev })
	t.Cleanup(func() { RegisterCacheResourceObserver(nil) })
	EmitCacheResource(CacheResourceEvent{Provider: "GOOGLEAI", Action: CacheResourceCreated, Tokens: 10})
	if got.Time.IsZero() || got.Tokens != 10 {
		t.Fatalf("event must reach the observer with a timestamp, got %+v", got)
	}
}
