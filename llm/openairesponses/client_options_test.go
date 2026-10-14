/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package openairesponses

import (
	"net/http"
	"testing"
	"time"

	"github.com/diillson/chatcli/auth"
	"github.com/diillson/chatcli/llm/catalog"
	"go.uber.org/zap"
)

func TestResponsesClientOptions(t *testing.T) {
	tp := auth.NewStaticTokenProvider("k", auth.AuthModeAPIKey, auth.ProviderOpenAI)
	wrapped := false
	c := NewOpenAIResponsesClientWithOptions(tp, "gw-model", zap.NewNop(), 1, time.Millisecond, ClientOptions{
		ResponsesURL:    "https://gw.example/v1/responses",
		WrapTransport:   func(next http.RoundTripper) http.RoundTripper { wrapped = true; return next },
		CatalogProvider: "acme_gw",
	})
	if c.responsesURL != "https://gw.example/v1/responses" || !wrapped || c.catalogName() != "ACME_GW" {
		t.Fatalf("options not applied")
	}
	plain := NewOpenAIResponsesClientWithOptions(tp, "gpt-x", zap.NewNop(), 1, time.Millisecond, ClientOptions{})
	if plain.catalogName() != catalog.ProviderOpenAI || plain.responsesURL != "" {
		t.Fatalf("zero options changed behavior")
	}
	_ = c.getMaxTokens()
}
