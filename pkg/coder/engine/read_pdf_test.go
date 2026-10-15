/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package engine

import (
	"context"
	"strings"
	"testing"
)

// A text read of a PDF returns its pages' text, not its bytes; base64 keeps
// the bytes.
func TestReadTextFileWithLimit_PDF(t *testing.T) {
	const fixture = "../../pdftext/testdata/report.pdf"
	text, truncated, err := readTextFileWithLimit(context.Background(), fixture, 0)
	if err != nil || truncated {
		t.Fatalf("err=%v truncated=%v", err, truncated)
	}
	if !strings.Contains(text, "[PDF report.pdf — 2 pages, extracted text]") || !strings.Contains(text, "--- page 2 ---") || strings.Contains(text, "endobj") {
		t.Fatalf("text = %.300s", text)
	}
	raw, _, err := readFileWithLimit(fixture, 0)
	if err != nil || !strings.HasPrefix(raw, "%PDF-") {
		t.Fatal("the raw read keeps the bytes")
	}
}
