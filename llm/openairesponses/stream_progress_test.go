/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package openairesponses

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/diillson/chatcli/llm/client"
	"go.uber.org/zap"
)

// The stream behind the buffered SendPrompt reports each piece of reply
// text as it is decoded, so a caller can show the reply arriving.
func TestProcessStreamResponse_ReportsTextAsItArrives(t *testing.T) {
	body := "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello \"}\n\n" +
		"data: {\"type\":\"response.reasoning_summary_text.delta\",\"delta\":\"hm\"}\n\n" +
		"data: {\"type\":\"response.reasoning_text.delta\",\"delta\":\"hmm\"}\n\n" +
		"data: {\"type\":\"response.function_call_arguments.delta\",\"delta\":\"{}\"}\n\n" +
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"world\"}\n\n" +
		"data: [DONE]\n\n"
	var pieces []int
	ctx := client.WithStreamProgress(context.Background(), func(chars int) { pieces = append(pieces, chars) })
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://example.invalid", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Request: req}
	c := NewOpenAIResponsesClient(testProvider("k"), "gpt-5", zap.NewNop(), 1, 0)
	text, err := c.processStreamResponse(resp)
	if err != nil || text != "hello world" {
		t.Fatalf("text %q, err %v", text, err)
	}
	// Both reasoning forms are counted (billed as output) without joining
	// the reply text; other deltas are not.
	// The decoder first says an attempt is starting.
	if len(pieces) != 5 || pieces[0] != client.StreamProgressRestart || pieces[1] != 6 || pieces[2] != 2 || pieces[3] != 3 || pieces[4] != 5 {
		t.Fatalf("reported pieces = %v, want [restart 6 2 3 5]", pieces)
	}
}
