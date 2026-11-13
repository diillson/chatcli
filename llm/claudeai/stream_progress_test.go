/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package claudeai

import (
	"context"
	"net/http"
	"testing"

	"github.com/diillson/chatcli/llm/client"
)

// The stream behind the buffered SendPrompt reports each piece of reply
// text as it is decoded, so a caller can show the reply arriving.
func TestProcessStreamResponse_ReportsTextAsItArrives(t *testing.T) {
	body := "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"hello \"}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"hm\"}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"world\"}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	var pieces []int
	ctx := client.WithStreamProgress(context.Background(), func(chars int) { pieces = append(pieces, chars) })
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://example.invalid", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp := &http.Response{StatusCode: http.StatusOK, Body: sseBody(body), Request: req}
	text, err := newEmptyReplyClient().processStreamResponse(resp, false)
	if err != nil || text != "hello world" {
		t.Fatalf("text %q, err %v", text, err)
	}
	// The thinking piece is counted (it is billed as output) but is not
	// part of the reply text.
	if len(pieces) != 3 || pieces[0] != 6 || pieces[1] != 2 || pieces[2] != 5 {
		t.Fatalf("reported pieces = %v, want [6 2 5]", pieces)
	}
}
