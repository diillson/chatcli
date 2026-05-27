/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package gateway

import (
	"testing"

	"go.uber.org/zap"
)

// TestAdapterNames pins each adapter's platform identity — the key the Runner
// uses to route replies back to the adapter that received a message.
func TestAdapterNames(t *testing.T) {
	cases := []struct {
		adapter Adapter
		want    string
	}{
		{NewTelegramAdapter("tok", []string{"1"}, zap.NewNop()), telegramPlatform},
		{NewDiscordAdapter("tok", zap.NewNop()), discordPlatform},
		{NewSlackAdapter("bot", "sec", ":0", "/x", zap.NewNop()), slackPlatform},
		{NewWhatsAppAdapter("tok", "phone", "verify", ":0", "/wa", zap.NewNop()), whatsappPlatform},
		{NewWebhookAdapter(":0", "/in", "secret", "http://cb", zap.NewNop()), webhookPlatform},
	}
	for _, c := range cases {
		if got := c.adapter.Name(); got != c.want {
			t.Errorf("Name() = %q, want %q", got, c.want)
		}
	}
}

// TestSeqHolder covers the Discord heartbeat sequence holder: nil before any
// dispatch, then the last sequence number once set.
func TestSeqHolder(t *testing.T) {
	var h seqHolder
	if got := h.get(); got != nil {
		t.Errorf("unset seqHolder should return nil, got %v", got)
	}
	h.set(42)
	if got := h.get(); got != int64(42) {
		t.Errorf("seqHolder.get() = %v, want 42", got)
	}
}

// TestCryptoFloat64Range pins that the heartbeat jitter source stays within
// [0,1) across many draws.
func TestCryptoFloat64Range(t *testing.T) {
	for i := 0; i < 1000; i++ {
		v := cryptoFloat64()
		if v < 0 || v >= 1 {
			t.Fatalf("cryptoFloat64 out of range: %v", v)
		}
	}
}
