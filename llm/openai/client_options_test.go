/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package openai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/diillson/chatcli/auth"
	"github.com/diillson/chatcli/llm/catalog"
	"go.uber.org/zap"
)

type headerInjector struct {
	next http.RoundTripper
}

func (h headerInjector) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("X-Gateway-Client", "client-123")
	return h.next.RoundTrip(r)
}

func TestClientOptionsTargetCustomEndpoint(t *testing.T) {
	t.Setenv("OPENAI_API_URL", "http://127.0.0.1:1/must-not-be-used")
	var gotPath, gotHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotHeader = r.URL.Path, r.Header.Get("X-Gateway-Client")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"pong"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`))
	}))
	defer srv.Close()

	c := NewOpenAIClientWithOptions(
		auth.NewStaticTokenProvider("unused", auth.AuthModeAPIKey, auth.ProviderOpenAI),
		"gw-model", zap.NewNop(), 1, time.Millisecond,
		ClientOptions{
			ChatURL:         srv.URL + "/v1/chat/completions",
			WrapTransport:   func(next http.RoundTripper) http.RoundTripper { return headerInjector{next: next} },
			CatalogProvider: "acme_gw",
		})
	out, err := c.SendPrompt(context.Background(), "ping", nil, 16)
	if err != nil {
		t.Fatal(err)
	}
	if out != "pong" || gotPath != "/v1/chat/completions" || gotHeader != "client-123" {
		t.Fatalf("out=%q path=%q header=%q", out, gotPath, gotHeader)
	}
	if c.catalogName() != "ACME_GW" {
		t.Fatalf("catalogName = %q", c.catalogName())
	}
}

func TestZeroOptionsKeepBuiltinBehavior(t *testing.T) {
	t.Setenv("OPENAI_API_URL", "https://example.test/v1/chat/completions")
	c := NewOpenAIClientWithOptions(nil, "gpt-x", zap.NewNop(), 1, time.Millisecond, ClientOptions{})
	if c.apiURL() != "https://example.test/v1/chat/completions" || c.catalogName() != catalog.ProviderOpenAI {
		t.Fatalf("apiURL=%q catalog=%q", c.apiURL(), c.catalogName())
	}
}
