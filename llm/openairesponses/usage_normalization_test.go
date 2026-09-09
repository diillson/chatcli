/*
 * ChatCLI - OpenAI Responses usage schema contract
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package openairesponses

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

// TestResponsesUsageIsSubset: the Responses schema renames the fields
// (input_tokens / input_tokens_details.cached_tokens) but keeps OpenAI's
// subset semantics.
func TestResponsesUsageIsSubset(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"output_text":"hi","usage":{"input_tokens":40000,"output_tokens":150,
			"input_tokens_details":{"cached_tokens":38000},
			"output_tokens_details":{"reasoning_tokens":90},"total_tokens":40150}}`)
	}))
	defer server.Close()
	t.Setenv("OPENAI_RESPONSES_API_URL", server.URL)

	c := NewOpenAIResponsesClient(testProvider("test-api-key"), "gpt-5.6-luna", zap.NewNop(), 1, 0)
	if _, err := c.SendPrompt(context.Background(), "Hi",
		[]models.Message{{Role: "user", Content: "Hi"}}, 0); err != nil {
		t.Fatalf("SendPrompt: %v", err)
	}
	u := c.LastUsage()
	if u == nil || u.InputTokensTotal != 40000 || u.TotalTokens != 40150 {
		t.Fatalf("usage not normalized: %+v", u)
	}
}
