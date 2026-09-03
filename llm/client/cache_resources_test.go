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

func TestPromptCacheTTL_AutoFollowsSurfaceHint(t *testing.T) {
	t.Setenv(PromptCacheTTLEnv, "auto")
	SetPromptCacheTTLHint("5m")
	if got := AnthropicCacheTTL(); got != "5m" {
		t.Fatalf("auto without an agent hint must be 5m, got %s", got)
	}
	SetPromptCacheTTLHint("1h")
	t.Cleanup(func() { SetPromptCacheTTLHint("5m") })
	if got := AnthropicCacheTTL(); got != "1h" {
		t.Fatalf("auto with the agent hint must be 1h, got %s", got)
	}
	if PromptCacheTTLDuration() != time.Hour {
		t.Fatal("duration must follow the resolved TTL")
	}
	// An explicit value ignores the hint.
	t.Setenv(PromptCacheTTLEnv, "5m")
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
