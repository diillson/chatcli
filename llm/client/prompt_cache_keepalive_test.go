package client

import (
	"context"
	"errors"
	"testing"
)

// While a route is kept warm by refreshes, "auto" keeps the 5-minute
// entry whatever the surface hint says; the held decision survives a
// round trip through Held/Restore, and a bad value resets to open.
func TestAnthropicCacheTTL_KeepAlivePreferenceAndHeldRoundTrip(t *testing.T) {
	t.Setenv(PromptCacheTTLEnv, "")
	t.Cleanup(func() {
		ResetPromptCacheTTL()
		SetPromptCacheKeepAlivePreferred(false)
		SetPromptCacheTTLHint("5m")
	})
	ResetPromptCacheTTL()
	SetPromptCacheTTLHint("1h")
	SetPromptCacheKeepAlivePreferred(true)
	if got := AnthropicCacheTTL(); got != "5m" {
		t.Fatalf("keep-alive preferred must hold the short entry, got %q", got)
	}
	if HeldPromptCacheTTL() != "5m" {
		t.Fatal("the resolved value must be held")
	}
	SetPromptCacheKeepAlivePreferred(false)
	ResetPromptCacheTTL()
	if got := AnthropicCacheTTL(); got != "1h" {
		t.Fatalf("without the preference the hint decides, got %q", got)
	}
	RestorePromptCacheTTL("5m")
	if HeldPromptCacheTTL() != "5m" {
		t.Fatal("restore must install the given decision")
	}
	RestorePromptCacheTTL("bogus")
	if HeldPromptCacheTTL() != "" {
		t.Fatal("an invalid value must reset to open")
	}
}

// The instrumented wrapper forwards to an inner client that can refresh
// and reports unsupported otherwise.
func TestInstrumentedClientForwardsKeepAlive(t *testing.T) {
	plain := NewInstrumentedClient(&MockLLMClient{}, plainRecorder{}, "MOCK")
	if _, err := plain.KeepPromptCacheWarm(context.Background()); !errors.Is(err, ErrPromptCacheKeepAliveUnsupported) {
		t.Fatalf("inner without keep-alive: got %v", err)
	}
	var none *InstrumentedClient
	if _, err := none.KeepPromptCacheWarm(context.Background()); !errors.Is(err, ErrPromptCacheKeepAliveUnsupported) {
		t.Fatalf("nil wrapper: got %v", err)
	}
}

// Describing the lifetime must not decide it: an open "auto" reads as
// empty and stays open, an explicit env or a held decision reads as is.
func TestPromptCacheTTLIfResolvedDoesNotSettleAuto(t *testing.T) {
	t.Setenv(PromptCacheTTLEnv, "")
	t.Cleanup(func() { ResetPromptCacheTTL(); SetPromptCacheTTLHint("5m") })
	ResetPromptCacheTTL()
	SetPromptCacheTTLHint("1h")
	if got := PromptCacheTTLIfResolved(); got != "" {
		t.Fatalf("open auto must read as empty, got %q", got)
	}
	if HeldPromptCacheTTL() != "" {
		t.Fatal("reading must not settle the decision")
	}
	if got := AnthropicCacheTTL(); got != "1h" || PromptCacheTTLIfResolved() != "1h" {
		t.Fatalf("once settled both agree, got %q", got)
	}
	t.Setenv(PromptCacheTTLEnv, "5m")
	if PromptCacheTTLIfResolved() != "5m" {
		t.Fatal("an explicit env reads as is")
	}
	t.Setenv(PromptCacheTTLEnv, "hour")
	if PromptCacheTTLIfResolved() != "1h" {
		t.Fatal("hour normalizes to 1h")
	}
}
