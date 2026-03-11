/*
 * ChatCLI - Paste Detection Tests
 * cli/paste/detector_test.go
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package paste

import (
	"testing"
)

func TestDetectInLine_NoPaste(t *testing.T) {
	cleaned, info := DetectInLine("normal input\n")
	if cleaned != "normal input\n" {
		t.Errorf("expected unchanged input, got %q", cleaned)
	}
	if info != nil {
		t.Error("expected nil info for normal input")
	}
}

func TestDetectInLine_WithPaste(t *testing.T) {
	startStr := string(pasteStartSeq)
	endStr := string(pasteEndSeq)

	line := startStr + "pasted content" + endStr + "\n"
	cleaned, info := DetectInLine(line)

	if cleaned != "pasted content\n" {
		t.Errorf("expected 'pasted content\\n', got %q", cleaned)
	}
	if info == nil {
		t.Fatal("expected paste info")
	}
	if info.CharCount != 14 {
		t.Errorf("expected 14 chars, got %d", info.CharCount)
	}
}

func TestDetectInLine_MultiLine(t *testing.T) {
	startStr := string(pasteStartSeq)
	endStr := string(pasteEndSeq)

	line := startStr + "line1\nline2\nline3" + endStr + "\n"
	cleaned, info := DetectInLine(line)

	if info == nil {
		t.Fatal("expected paste info")
	}
	if info.LineCount != 4 { // "line1\nline2\nline3\n" = 4 lines
		t.Errorf("expected 4 lines, got %d", info.LineCount)
	}
	_ = cleaned
}
