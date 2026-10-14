/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package providerhost

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/diillson/chatcli/llm/client"
)

// Each dialect drives chatcli's built-in client for that wire format
// against the plugin's backend: the right endpoint, the plugin's headers,
// no placeholder credential, and usage (cache included) parsed by the
// built-in parser.
func TestDialectsDriveBuiltinClients(t *testing.T) {
	cases := []struct {
		suffix, path, body string
	}{
		{"-anthropic", "/v1/messages",
			`{"id":"m","type":"message","role":"assistant","content":[{"type":"text","text":"pong"}],"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":1,"cache_read_input_tokens":4}}`},
		{"-gemini", "/v1/models/gw-sonnet:generateContent",
			`{"candidates":[{"content":{"role":"model","parts":[{"text":"pong"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":1,"totalTokenCount":11,"cachedContentTokenCount":4}}`},
		{"-responses", "/v1/responses",
			`{"id":"r","object":"response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"pong"}]}],"output_text":"pong","usage":{"input_tokens":10,"output_tokens":1,"total_tokens":11,"input_tokens_details":{"cached_tokens":4}}}`},
	}
	for _, tc := range cases {
		t.Run(strings.TrimPrefix(tc.suffix, "-"), func(t *testing.T) {
			var mu sync.Mutex
			var gotPath string
			var gotHeaders http.Header
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				gotPath, gotHeaders = r.URL.Path, r.Header.Clone()
				mu.Unlock()
				if r.URL.Path != tc.path {
					http.NotFound(w, r)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.body))
			}))
			defer backend.Close()

			dir := t.TempDir()
			installFixture(t, dir, "ph-dialect"+tc.suffix)
			if err := os.WriteFile(filepath.Join(dir, "backend-url"), []byte(backend.URL), 0o600); err != nil {
				t.Fatal(err)
			}
			set := loadSet(t, dir, newMemStore())
			p, ok := set.Get("PH_GATEWAY")
			if !ok {
				t.Fatalf("plugin not loaded: %v", set.Errors())
			}
			ctx := context.Background()
			if _, err := p.Login(ctx, nil, map[string]string{"token": "tok"}); err != nil {
				t.Fatal(err)
			}
			if _, err := p.Models(ctx); err != nil {
				t.Fatal(err)
			}
			c, err := p.NewClient("gw-sonnet")
			if err != nil {
				t.Fatal(err)
			}
			out, err := c.SendPrompt(ctx, "ping", nil, 64)
			mu.Lock()
			defer mu.Unlock()
			if err != nil || out != "pong" {
				t.Fatalf("SendPrompt = %q, %v (backend saw %s)", out, err, gotPath)
			}
			if !strings.HasPrefix(gotHeaders.Get("Authorization"), "Bearer tok-") {
				t.Fatalf("plugin bearer missing: %v", gotHeaders)
			}
			for _, h := range []string{"X-Api-Key", "X-Goog-Api-Key"} {
				if v := gotHeaders.Get(h); v != "" {
					t.Fatalf("%s leaked the placeholder credential: %q", h, v)
				}
			}
			if gotHeaders.Get("X-Route") != "anthropic" {
				t.Fatalf("per-model header missing: %v", gotHeaders)
			}
			if u := c.(client.UsageAwareClient).LastUsage(); u == nil || u.CacheReadInputTokens != 4 {
				t.Fatalf("usage = %+v, want cached tokens from the built-in parser", u)
			}
			if lm, err := c.(client.ModelLister).ListModels(ctx); err != nil || len(lm) != 1 || lm[0].ID != "gw-sonnet" {
				t.Fatalf("ListModels = %+v, %v (must come from the plugin)", lm, err)
			}
		})
	}
}
