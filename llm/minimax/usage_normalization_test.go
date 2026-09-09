/*
 * ChatCLI - MiniMax usage schema contract
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * MiniMax is the case that proves the schema is not a property of the model
 * name: the SAME model reports OpenAI-shaped counts on the Chat Completions
 * surface and Anthropic-shaped ones on the Anthropic-compatible surface. A
 * heuristic keyed on "claude" in the model string gets the second one wrong;
 * the adapter that read the payload does not.
 */
package minimax

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/diillson/chatcli/models"
)

func TestMiniMaxOpenAISurfaceIsSubset(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":5000,"completion_tokens":30,"total_tokens":5030,
			"prompt_tokens_details":{"cached_tokens":4500}}}`))
	}))
	defer server.Close()

	c := newTestClient(server.URL)
	if _, err := c.SendPrompt(context.Background(), "Hi",
		[]models.Message{{Role: "user", Content: "Hi"}}, 0); err != nil {
		t.Fatalf("SendPrompt: %v", err)
	}
	if u := c.LastUsage(); u == nil || u.InputTokensTotal != 5000 {
		t.Fatalf("usage not normalized: %+v", u)
	}
}

// TestMiniMaxAnthropicSurfaceIsAdditive: same client, same model name, the
// other endpoint — and here cache reads/writes sit BESIDE input_tokens. The
// old model-name heuristic classified this as subset and undercounted the
// input by the whole cached prefix.
func TestMiniMaxAnthropicSurfaceIsAdditive(t *testing.T) {
	c := newTestClient("http://unused.invalid")
	body := `{"content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn",
		"usage":{"input_tokens":400,"output_tokens":20,
		"cache_creation_input_tokens":1000,"cache_read_input_tokens":9000}}`
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
	}
	if _, err := c.processAnthropicResponse(resp); err != nil {
		t.Fatalf("processAnthropicResponse: %v", err)
	}
	u := c.LastUsage()
	if u == nil || u.InputTokensTotal != 10400 {
		t.Fatalf("InputTokensTotal = %+v, want 10400", u)
	}
}
