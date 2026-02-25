/*
 * ChatCLI - Paste Detection Tests
 * cli/paste/detector_test.go
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
 */
package paste

import (
	"testing"

	prompt "github.com/c-bata/go-prompt"
)

// mockParser is a mock ConsoleParser for testing.
type mockParser struct {
	reads   [][]byte
	readIdx int
}

func (m *mockParser) Setup() error              { return nil }
func (m *mockParser) TearDown() error            { return nil }
func (m *mockParser) GetWinSize() *prompt.WinSize { return &prompt.WinSize{Row: 24, Col: 80} }
func (m *mockParser) Read() ([]byte, error) {
	if m.readIdx >= len(m.reads) {
		// Block forever (shouldn't happen in tests)
		select {}
	}
	data := m.reads[m.readIdx]
	m.readIdx++
	return data, nil
}

func TestBracketedPasteParser_NoPaste(t *testing.T) {
	mock := &mockParser{reads: [][]byte{[]byte("hello")}}
	var gotPaste *Info

	parser := NewBracketedPasteParser(mock, func(info Info) {
		gotPaste = &info
	})
	parser.enabled = true

	data := parser.processData([]byte("hello"))

	if string(data) != "hello" {
		t.Errorf("expected 'hello', got %q", string(data))
	}
	if gotPaste != nil {
		t.Error("expected no paste detection")
	}
}

func TestBracketedPasteParser_CompletePasteInSingleRead(t *testing.T) {
	var gotPaste *Info

	parser := NewBracketedPasteParser(nil, func(info Info) {
		gotPaste = &info
	})
	parser.enabled = true

	// Simulate a complete paste: ESC[200~ content ESC[201~
	input := append([]byte{}, pasteStartSeq...)
	input = append(input, []byte("pasted text")...)
	input = append(input, pasteEndSeq...)

	data := parser.processData(input)

	if string(data) != "pasted text" {
		t.Errorf("expected 'pasted text', got %q", string(data))
	}
	if gotPaste == nil {
		t.Fatal("expected paste detection")
	}
	if gotPaste.CharCount != 11 {
		t.Errorf("expected 11 chars, got %d", gotPaste.CharCount)
	}
	if gotPaste.LineCount != 1 {
		t.Errorf("expected 1 line, got %d", gotPaste.LineCount)
	}
}

func TestBracketedPasteParser_MultiLinePaste(t *testing.T) {
	var gotPaste *Info

	parser := NewBracketedPasteParser(nil, func(info Info) {
		gotPaste = &info
	})
	parser.enabled = true

	content := "line1\nline2\nline3"
	input := append([]byte{}, pasteStartSeq...)
	input = append(input, []byte(content)...)
	input = append(input, pasteEndSeq...)

	data := parser.processData(input)

	if string(data) != content {
		t.Errorf("expected %q, got %q", content, string(data))
	}
	if gotPaste == nil {
		t.Fatal("expected paste detection")
	}
	if gotPaste.LineCount != 3 {
		t.Errorf("expected 3 lines, got %d", gotPaste.LineCount)
	}
}

func TestBracketedPasteParser_PasteAcrossMultipleReads(t *testing.T) {
	var gotPaste *Info

	parser := NewBracketedPasteParser(nil, func(info Info) {
		gotPaste = &info
	})
	parser.enabled = true

	// First read: start sequence + partial content
	data1 := parser.processData(append(append([]byte{}, pasteStartSeq...), []byte("part1")...))
	if data1 != nil {
		t.Errorf("expected nil during paste buffering, got %q", string(data1))
	}

	// Second read: more content
	data2 := parser.processData([]byte("part2"))
	if data2 != nil {
		t.Errorf("expected nil during paste buffering, got %q", string(data2))
	}

	// Third read: end of content + end sequence
	data3 := parser.processData(append([]byte("part3"), pasteEndSeq...))
	if string(data3) != "part1part2part3" {
		t.Errorf("expected 'part1part2part3', got %q", string(data3))
	}

	if gotPaste == nil {
		t.Fatal("expected paste detection")
	}
	if gotPaste.CharCount != 15 {
		t.Errorf("expected 15 chars, got %d", gotPaste.CharCount)
	}
}

func TestBracketedPasteParser_TextBeforeAndAfterPaste(t *testing.T) {
	var gotPaste *Info

	parser := NewBracketedPasteParser(nil, func(info Info) {
		gotPaste = &info
	})
	parser.enabled = true

	// Text before paste start + paste + text after paste end
	input := []byte("before")
	input = append(input, pasteStartSeq...)
	input = append(input, []byte("pasted")...)
	input = append(input, pasteEndSeq...)
	input = append(input, []byte("after")...)

	data := parser.processData(input)

	if string(data) != "beforepasted" {
		t.Errorf("expected 'beforepasted', got %q", string(data))
	}
	if gotPaste == nil {
		t.Fatal("expected paste detection")
	}
	if gotPaste.CharCount != 6 {
		t.Errorf("expected 6 chars, got %d", gotPaste.CharCount)
	}

	// "after" should be in pending
	parser.mu.Lock()
	pending := parser.pending
	parser.mu.Unlock()
	if string(pending) != "after" {
		t.Errorf("expected 'after' in pending, got %q", string(pending))
	}
}

func TestBracketedPasteParser_DisabledPassthrough(t *testing.T) {
	parser := NewBracketedPasteParser(nil, func(info Info) {
		t.Error("should not be called when disabled")
	})
	parser.enabled = false

	// Even with paste sequences, data should pass through unchanged
	input := append([]byte{}, pasteStartSeq...)
	input = append(input, []byte("content")...)
	input = append(input, pasteEndSeq...)

	mock := &mockParser{reads: [][]byte{input}}
	parser.inner = mock

	data, err := parser.Read()
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(input) {
		t.Errorf("expected passthrough when disabled, got different data")
	}
}

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

func TestBracketedPasteParser_EmptyPaste(t *testing.T) {
	var gotPaste *Info

	parser := NewBracketedPasteParser(nil, func(info Info) {
		gotPaste = &info
	})
	parser.enabled = true

	// Empty paste: ESC[200~ ESC[201~
	input := append(append([]byte{}, pasteStartSeq...), pasteEndSeq...)

	data := parser.processData(input)

	if len(data) != 0 {
		t.Errorf("expected empty data for empty paste, got %q", string(data))
	}
	if gotPaste == nil {
		t.Fatal("expected paste detection even for empty paste")
	}
	if gotPaste.CharCount != 0 {
		t.Errorf("expected 0 chars, got %d", gotPaste.CharCount)
	}
}

func TestBracketedPasteParser_UnicodeContent(t *testing.T) {
	var gotPaste *Info

	parser := NewBracketedPasteParser(nil, func(info Info) {
		gotPaste = &info
	})
	parser.enabled = true

	content := "olá mundo 🌍"
	input := append([]byte{}, pasteStartSeq...)
	input = append(input, []byte(content)...)
	input = append(input, pasteEndSeq...)

	data := parser.processData(input)

	if string(data) != content {
		t.Errorf("expected %q, got %q", content, string(data))
	}
	if gotPaste == nil {
		t.Fatal("expected paste detection")
	}
	// "olá mundo 🌍" = 11 runes (o, l, á, ' ', m, u, n, d, o, ' ', 🌍)
	if gotPaste.CharCount != 11 {
		t.Errorf("expected 11 chars (runes), got %d", gotPaste.CharCount)
	}
}
