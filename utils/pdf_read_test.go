/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package utils

import (
	"strings"
	"testing"
)

// /context and @file directory scans read a PDF as its text, with the size
// cap applied to that text.
func TestReadFileContent_PDF(t *testing.T) {
	text, err := ReadFileContent("../pkg/pdftext/testdata/report.pdf", 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "Ação corretiva") || strings.Contains(text, "%PDF") {
		t.Fatalf("text = %.200s", text)
	}
	short, err := ReadFileContent("../pkg/pdftext/testdata/report.pdf", 30)
	if err != nil || len(short) != 30 {
		t.Fatalf("cap: len=%d err=%v", len(short), err)
	}
	if DetectFileType("x/report.pdf") != "PDF" {
		t.Fatal("DetectFileType")
	}
}
