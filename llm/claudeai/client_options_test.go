/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package claudeai

import (
	"net/http"
	"testing"
	"time"

	"github.com/diillson/chatcli/auth"
	"github.com/diillson/chatcli/llm/catalog"
	"go.uber.org/zap"
)

func TestClaudeClientOptions(t *testing.T) {
	tp := auth.NewStaticTokenProvider("k", auth.AuthModeAPIKey, auth.ProviderAnthropic)
	wrapped := false
	c := NewClaudeClientWithOptions(tp, "gw-sonnet", zap.NewNop(), 1, time.Millisecond, ClientOptions{
		MessagesURL:     "https://gw.example/v1/messages",
		WrapTransport:   func(next http.RoundTripper) http.RoundTripper { wrapped = true; return next },
		CatalogProvider: "acme_gw",
		UpstreamModel:   "Claude-Sonnet-4-6",
	})
	if c.apiURL != "https://gw.example/v1/messages" || !wrapped || c.catalogName() != "ACME_GW" || c.capModel() != "claude-sonnet-4-6" {
		t.Fatalf("options not applied: url=%q catalog=%q cap=%q wrapped=%v", c.apiURL, c.catalogName(), c.capModel(), wrapped)
	}
	plain := NewClaudeClientWithOptions(tp, "claude-x", zap.NewNop(), 1, time.Millisecond, ClientOptions{})
	if plain.catalogName() != catalog.ProviderClaudeAI || plain.capModel() != "claude-x" {
		t.Fatalf("zero options changed behavior: %q %q", plain.catalogName(), plain.capModel())
	}
	_ = plain.getMaxTokens()
	_ = c.getMaxTokens()
}
