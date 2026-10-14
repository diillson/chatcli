/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package googleai

import (
	"net/http"
	"testing"
	"time"

	"github.com/diillson/chatcli/auth"
	"github.com/diillson/chatcli/config"
	"github.com/diillson/chatcli/llm/catalog"
	"go.uber.org/zap"
)

func TestGeminiClientOptions(t *testing.T) {
	tp := auth.NewStaticTokenProvider("k", auth.AuthModeAPIKey, auth.ProviderID("googleai"))
	wrapped := false
	c := NewGeminiClientWithOptions(tp, "gw-flash", zap.NewNop(), 1, time.Millisecond, ClientOptions{
		BaseURL:         "https://gw.example/v1beta/",
		WrapTransport:   func(next http.RoundTripper) http.RoundTripper { wrapped = true; return next },
		CatalogProvider: "acme_gw",
	})
	if c.baseURL != "https://gw.example/v1beta" || !wrapped || c.catalogName() != "ACME_GW" {
		t.Fatalf("options not applied: base=%q catalog=%q wrapped=%v", c.baseURL, c.catalogName(), wrapped)
	}
	plain := NewGeminiClientWithOptions(tp, "gemini-x", zap.NewNop(), 1, time.Millisecond, ClientOptions{})
	if plain.baseURL != config.GoogleAIAPIURL || plain.catalogName() != catalog.ProviderGoogleAI {
		t.Fatalf("zero options changed behavior")
	}
	_ = c.getMaxTokens()
}
