/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package claudeai

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
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
	// The decoder first says an attempt is starting.
	if len(pieces) != 4 || pieces[0] != client.StreamProgressRestart || pieces[1] != 6 || pieces[2] != 2 || pieces[3] != 5 {
		t.Fatalf("reported pieces = %v, want [restart 6 2 5]", pieces)
	}
}

// A retried attempt runs the decoder again on a new response: the sink is
// told to start over before the second attempt's text, so a caller adding
// the pieces up never counts the abandoned attempt.
func TestProcessStreamResponse_RetryStartsTheCountOver(t *testing.T) {
	total := 0
	ctx := client.WithStreamProgress(context.Background(), func(chars int) {
		if chars == client.StreamProgressRestart {
			total = 0
			return
		}
		total += chars
	})
	attempt := func(text string) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://example.invalid", nil)
		if err != nil {
			t.Fatal(err)
		}
		body := fmt.Sprintf("data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":%q}}\n\ndata: {\"type\":\"message_stop\"}\n\n", text)
		if _, err := newEmptyReplyClient().processStreamResponse(&http.Response{StatusCode: http.StatusOK, Body: sseBody(body), Request: req}, true); err != nil {
			t.Fatal(err)
		}
	}
	attempt("a partial reply that was cut")
	attempt("the reply")
	if total != len("the reply") {
		t.Fatalf("after a retry the count is %d, want %d (the second attempt alone)", total, len("the reply"))
	}
}

// The OAuth title request streams under the turn's context, but its text is
// not the reply: the turn's sink must not see it.
func TestOAuthTitleRequestIsNotCountedAsReply(t *testing.T) {
	served := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		served = true
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"A title for the session\"}}\n\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer server.Close()
	c := newEmptyReplyClient()
	c.apiURL = server.URL

	var seen []int
	ctx := client.WithStreamProgress(context.Background(), func(chars int) { seen = append(seen, chars) })
	if err := c.sendOAuthTitleRequest(ctx, "name this session"); err != nil {
		t.Fatalf("title request: %v", err)
	}
	if !served {
		t.Fatal("the title request never reached the server")
	}
	if len(seen) != 0 {
		t.Fatalf("the title request fed the turn's sink: %v", seen)
	}
}
