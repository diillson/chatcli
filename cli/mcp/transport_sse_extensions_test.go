/*
 * ChatCLI - Tests for SSE transport behavior added by config extensions
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * The full SSE bring-up needs a real HTTP server; that path is
 * covered by the existing manager_extensions tests via integration.
 * Here we lock down the header / auth injection helper in isolation,
 * which is what users actually configure and care about.
 */
package mcp

import (
	"net/http"
	"testing"
	"time"
)

func TestSseTransport_ApplyHeaders_InjectsCustomHeadersAndAuth(t *testing.T) {
	t.Setenv("MY_TOKEN", "abc.def")
	tp := &sseTransport{
		headers: map[string]string{
			"X-Static": "literal",
			"X-Token":  "Token123", // already resolved by ResolveHeaders upstream
		},
		auth: &AuthConfig{Type: "bearer", Token: "${MY_TOKEN}"},
	}
	req, _ := http.NewRequest("GET", "http://example/sse", nil)
	tp.applyHeaders(req)

	if got := req.Header.Get("X-Static"); got != "literal" {
		t.Errorf("X-Static = %q, want literal", got)
	}
	if got := req.Header.Get("X-Token"); got != "Token123" {
		t.Errorf("X-Token = %q, want Token123", got)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer abc.def" {
		t.Errorf("Authorization = %q, want Bearer abc.def", got)
	}
}

func TestSseTransport_ApplyHeaders_NoConfigIsNoop(t *testing.T) {
	tp := &sseTransport{}
	req, _ := http.NewRequest("GET", "http://example/sse", nil)
	tp.applyHeaders(req) // must not panic
	if len(req.Header) != 0 {
		t.Errorf("empty config should not add headers; got %v", req.Header)
	}
}

func TestMaxDuration_PicksLarger(t *testing.T) {
	cases := []struct{ a, b, want time.Duration }{
		{5 * time.Second, 10 * time.Second, 10 * time.Second},
		{10 * time.Second, 5 * time.Second, 10 * time.Second},
		{0, 7 * time.Second, 7 * time.Second},
		{3 * time.Second, 3 * time.Second, 3 * time.Second},
	}
	for _, tc := range cases {
		if got := maxDuration(tc.a, tc.b); got != tc.want {
			t.Errorf("maxDuration(%s, %s) = %s, want %s", tc.a, tc.b, got, tc.want)
		}
	}
}
