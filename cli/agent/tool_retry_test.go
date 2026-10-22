/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestIsTransientToolError(t *testing.T) {
	transient := []error{
		errors.New("Get \"https://x\": dial tcp: i/o timeout"),
		errors.New("read tcp: connection reset by peer"),
		errors.New("@webfetch: HTTP 503 Service Unavailable"),
		errors.New("status 429 Too Many Requests"),
		errors.New("unexpected EOF"),
		errors.New("lookup api.example.com: no such host"),
		fmt.Errorf("wrapped: %w", context.DeadlineExceeded),
	}
	for _, err := range transient {
		if !IsTransientToolError(err) {
			t.Errorf("%v must be transient", err)
		}
	}
	permanent := []error{
		nil,
		context.Canceled,
		fmt.Errorf("plugin: %w", context.Canceled),
		errors.New("HTTP 404 Not Found"),
		errors.New("no element matches \"#x\""),
		errors.New("argumento obrigatório faltando: file"),
		errors.New("dangerous command blocked: rm -rf /"),
	}
	for _, err := range permanent {
		if IsTransientToolError(err) {
			t.Errorf("%v must NOT be transient", err)
		}
	}
}

func TestBatchNotes(t *testing.T) {
	skipped := BatchSkippedNote(3, "@coder")
	for _, want := range []string{"Action 3 (@coder) not executed", "earlier action of this batch failed", "Read-only actions of the batch still ran"} {
		if !strings.Contains(skipped, want) {
			t.Fatalf("skipped note missing %q: %s", want, skipped)
		}
	}
	long := errors.New(strings.Repeat("x", 300))
	retried := BatchRetriedNote(long)
	if !strings.Contains(retried, "retried once") || len(retried) > 300 {
		t.Fatalf("retried note = %q", retried)
	}
	if !strings.HasSuffix(retried, "\n") {
		t.Fatal("retried note must end the line so the result starts fresh")
	}
}
