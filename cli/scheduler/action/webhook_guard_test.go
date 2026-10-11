/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package action

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/diillson/chatcli/cli/scheduler"
)

// A stored webhook URL that redirects to the cloud metadata service must
// be refused by the redirect guard, not followed.
func TestWebhook_RefusesRedirectToMetadata(t *testing.T) {
	t.Setenv("CHATCLI_WEBFETCH_BLOCK_PRIVATE", "")
	t.Setenv("HTTP_PROXY", "")
	t.Setenv("HTTPS_PROXY", "")
	t.Setenv("ALL_PROXY", "")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://169.254.169.254/latest/meta-data/", http.StatusTemporaryRedirect)
	}))
	defer srv.Close()

	res := NewWebhook().Execute(context.Background(), scheduler.Action{
		Type:    scheduler.ActionWebhook,
		Payload: map[string]any{"url": srv.URL, "timeout": "3s"},
	}, nil)
	if res.Err == nil || !strings.Contains(res.Err.Error(), "refusing redirect") {
		t.Fatalf("redirect to metadata must be refused by the guard, got %v", res.Err)
	}
}

func TestWebhook_DirectMetadataTargetRefused(t *testing.T) {
	res := NewWebhook().Execute(context.Background(), scheduler.Action{
		Type:    scheduler.ActionWebhook,
		Payload: map[string]any{"url": "http://169.254.169.254/latest/meta-data/", "timeout": "3s"},
	}, nil)
	if res.Err == nil || !strings.Contains(res.Err.Error(), "blocked") {
		t.Fatalf("metadata target must be blocked at dial time, got %v", res.Err)
	}
}
