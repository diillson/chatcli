/*
 * ChatCLI - Gemini usage schema contract
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package googleai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

// TestGeminiUsageIsSubset: cachedContentTokenCount is the cached SHARE of
// promptTokenCount, so the normalized input is promptTokenCount itself.
func TestGeminiUsageIsSubset(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}],
			"usageMetadata":{"promptTokenCount":9000,"candidatesTokenCount":120,
			"cachedContentTokenCount":8000,"thoughtsTokenCount":300,"totalTokenCount":9420}}`))
	}))
	defer server.Close()

	c := NewGeminiClient(testProvider("k"), "gemini-3-pro", zap.NewNop(), 1, 0)
	c.baseURL = server.URL
	if _, err := c.SendPrompt(context.Background(), "Hi",
		[]models.Message{{Role: "user", Content: "Hi"}}, 0); err != nil {
		t.Fatalf("SendPrompt: %v", err)
	}

	u := c.LastUsage()
	if u == nil {
		t.Fatal("no usage captured")
	}
	if u.InputTokensTotal != 9000 {
		t.Fatalf("InputTokensTotal = %d, want 9000 (cached is already inside)", u.InputTokensTotal)
	}
	// Gemini folds thoughtsTokenCount into its own total — normalization may
	// raise a total, never shrink it.
	if u.TotalTokens != 9420 {
		t.Fatalf("TotalTokens = %d, want the provider's 9420", u.TotalTokens)
	}
}
