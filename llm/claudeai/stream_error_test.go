/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package claudeai

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/andybalholm/brotli"
	"github.com/diillson/chatcli/auth"
	"github.com/diillson/chatcli/utils"
	"github.com/klauspost/compress/zstd"
	"go.uber.org/zap"
)

func TestProcessStreamResponse_ErrorEventBecomesRetryableAPIError(t *testing.T) {
	body := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":10}}}\n\n" +
		"event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Overloaded\"}}\n\n"
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body))}
	c := NewClaudeClient(auth.NewStaticTokenProvider("t", auth.AuthModeAPIKey, auth.ProviderID("claudeai")), "claude-sonnet-4-6", zap.NewNop(), 1, time.Millisecond)
	_, err := c.processStreamResponse(resp, true)
	var apiErr *utils.APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 529 || !strings.Contains(apiErr.Message, "Overloaded") {
		t.Fatalf("overloaded SSE event must become a 529 APIError with the message, got %v", err)
	}
	if !utils.IsTemporaryError(err) {
		t.Fatal("an overload must be retryable")
	}
	// A normal stream still yields its text.
	ok := "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	resp = &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(ok))}
	if text, err := c.processStreamResponse(resp, false); err != nil || text != "hello" {
		t.Fatalf("text stream: %q %v", text, err)
	}
}

func TestStreamErrorToAPIError_Mapping(t *testing.T) {
	for typ, want := range map[string]int{"overloaded_error": 529, "rate_limit_error": 429, "authentication_error": 401,
		"invalid_request_error": 400, "api_error": 500, "request_too_large": 413, "not_found_error": 404} {
		var apiErr *utils.APIError
		if err := streamErrorToAPIError(typ, "m"); !errors.As(err, &apiErr) || apiErr.StatusCode != want {
			t.Fatalf("%s → %v, want %d", typ, err, want)
		}
	}
}

func TestDecodeResponseBody_BrotliAndZstd(t *testing.T) {
	var br bytes.Buffer
	bw := brotli.NewWriter(&br)
	_, _ = bw.Write([]byte(`{"ok":"br"}`))
	_ = bw.Close()
	resp := &http.Response{Header: http.Header{"Content-Encoding": []string{"br"}}, Body: io.NopCloser(bytes.NewReader(br.Bytes()))}
	rc, err := decodeResponseBody(resp)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := io.ReadAll(rc); string(b) != `{"ok":"br"}` {
		t.Fatalf("brotli decode = %q", b)
	}
	_ = rc.Close()

	var zb bytes.Buffer
	zw, _ := zstd.NewWriter(&zb)
	_, _ = zw.Write([]byte(`{"ok":"zstd"}`))
	_ = zw.Close()
	resp = &http.Response{Header: http.Header{"Content-Encoding": []string{"zstd"}}, Body: io.NopCloser(bytes.NewReader(zb.Bytes()))}
	rc, err = decodeResponseBody(resp)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := io.ReadAll(rc); string(b) != `{"ok":"zstd"}` {
		t.Fatalf("zstd decode = %q", b)
	}
	_ = rc.Close()
}
