/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package condition

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/diillson/chatcli/cli/scheduler"
)

func TestHTTPStatus_RefusesMetadata(t *testing.T) {
	t.Setenv("CHATCLI_WEBFETCH_BLOCK_PRIVATE", "")
	t.Setenv("HTTP_PROXY", "")
	t.Setenv("HTTPS_PROXY", "")
	t.Setenv("ALL_PROXY", "")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://169.254.169.254/latest/meta-data/", http.StatusFound)
	}))
	defer srv.Close()

	h := NewHTTPStatus()
	out := h.Evaluate(context.Background(), scheduler.Condition{Type: "http_status", Spec: map[string]any{"url": srv.URL, "timeout": "3s"}}, nil)
	if out.Err == nil || !strings.Contains(out.Err.Error(), "refusing redirect") {
		t.Fatalf("redirect to metadata must be refused, got %+v", out)
	}
	out = h.Evaluate(context.Background(), scheduler.Condition{Type: "http_status", Spec: map[string]any{"url": "http://169.254.169.254/", "timeout": "3s"}}, nil)
	if out.Err == nil || !strings.Contains(out.Err.Error(), "blocked") {
		t.Fatalf("metadata target must be blocked, got %+v", out)
	}
}

// A local service stays reachable: private ranges are allowed unless
// CHATCLI_WEBFETCH_BLOCK_PRIVATE is set.
func TestHTTPStatus_LocalServiceStillWorks(t *testing.T) {
	t.Setenv("CHATCLI_WEBFETCH_BLOCK_PRIVATE", "")
	t.Setenv("HTTP_PROXY", "")
	t.Setenv("HTTPS_PROXY", "")
	t.Setenv("ALL_PROXY", "")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer srv.Close()
	out := NewHTTPStatus().Evaluate(context.Background(), scheduler.Condition{Type: "http_status", Spec: map[string]any{"url": srv.URL}}, nil)
	if out.Err != nil || !out.Satisfied {
		t.Fatalf("local probe: %+v", out)
	}
}
