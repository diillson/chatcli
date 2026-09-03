/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package agent

import (
	"fmt"
	"strings"
	"testing"

	"github.com/diillson/chatcli/cli/compress"
	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

func readResult(path string, first, last int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<<< INÍCIO DO ARQUIVO: %s >>>\n", path)
	for i := first; i <= last; i++ {
		fmt.Fprintf(&b, "%4d | line %d of %s\n", i, i, path)
	}
	fmt.Fprintf(&b, "<<< FIM DO ARQUIVO: %s >>>\n\n", path)
	return b.String()
}

func TestParseReadBlocks(t *testing.T) {
	content := readResult("a.go", 1, 40) + readResult("b.go", 10, 20)
	blocks := parseReadBlocks(content)
	if len(blocks) != 2 || blocks[0].Path != "a.go" || blocks[0].FirstLine != 1 || blocks[0].LastLine != 40 ||
		blocks[1].Path != "b.go" || blocks[1].FirstLine != 10 || blocks[1].LastLine != 20 {
		t.Fatalf("blocks = %+v", blocks)
	}
	b64 := "<<< INÍCIO DO ARQUIVO (base64): x.bin >>>\nAAAA\n<<< FIM DO ARQUIVO: x.bin >>>\n"
	if blocks := parseReadBlocks(b64); len(blocks) != 1 || !blocks[0].Whole {
		t.Fatalf("base64 block = %+v", blocks)
	}
	if parseReadBlocks("plain exec output") != nil {
		t.Fatal("non-read content must not parse")
	}
}

func TestDedupRepeatedReads_LaterFullReadSupersedesEarlier(t *testing.T) {
	layer := compress.NewLayer(compress.Config{Mode: compress.ModeLossyWithCCR, Store: compress.NewMemoryStore(), Threshold: 100})
	first := readResult("main.go", 1, 50)
	history := []models.Message{
		{Role: "user", Content: "read main.go"},
		{Role: "assistant", Content: "reading"},
		{Role: "tool", Content: first, ToolCallID: "t1"},
		{Role: "user", Content: "again"},
		{Role: "assistant", Content: "reading"},
		{Role: "tool", Content: readResult("main.go", 1, 50), ToolCallID: "t2"},
		{Role: "user", Content: "next"},
	}
	got, report := DedupRepeatedReads(history, layer, zap.NewNop())
	if report.Superseded != 1 {
		t.Fatalf("expected 1 superseded, got %+v", report)
	}
	if !strings.Contains(got[2].Content, "superseded") || !strings.Contains(got[2].Content, "<<ccr:") {
		t.Fatalf("older read not stubbed with recall marker: %q", got[2].Content)
	}
	if got[5].Content != readResult("main.go", 1, 50) {
		t.Fatal("newest read must stay verbatim")
	}
	keys := compress.ExtractKeys(got[2].Content)
	if orig, ok := layer.Recall(keys[0]); !ok || orig != first {
		t.Fatal("original read must be recoverable via CCR")
	}
}

func TestDedupRepeatedReads_NarrowerLaterReadDoesNotSupersede(t *testing.T) {
	history := []models.Message{
		{Role: "tool", Content: readResult("main.go", 1, 200), ToolCallID: "t1"},
		{Role: "user", Content: "x"},
		{Role: "tool", Content: readResult("main.go", 10, 20), ToolCallID: "t2"},
		{Role: "user", Content: "y"},
	}
	_, report := DedupRepeatedReads(history, nil, nil)
	if report.Superseded != 0 {
		t.Fatalf("a narrower later read must not supersede a wider one: %+v", report)
	}
	// The other direction (later read is wider) does supersede.
	history2 := []models.Message{
		{Role: "tool", Content: readResult("main.go", 10, 20), ToolCallID: "t1"},
		{Role: "user", Content: "x"},
		{Role: "tool", Content: readResult("main.go", 1, 200), ToolCallID: "t2"},
		{Role: "user", Content: "y"},
	}
	if _, r := DedupRepeatedReads(history2, nil, nil); r.Superseded != 1 {
		t.Fatalf("wider later read must supersede: %+v", r)
	}
}

func TestDedupRepeatedReads_RespectsPreserveAndDifferentFiles(t *testing.T) {
	history := []models.Message{
		{Role: "tool", Content: readResult("a.go", 1, 10), ToolCallID: "t1", Meta: &models.MessageMeta{PreserveVerbatim: true}},
		{Role: "user", Content: "x"},
		{Role: "tool", Content: readResult("a.go", 1, 10), ToolCallID: "t2"},
		{Role: "user", Content: "y"},
		{Role: "tool", Content: readResult("b.go", 1, 10), ToolCallID: "t3"},
		{Role: "user", Content: "z"},
	}
	_, report := DedupRepeatedReads(history, nil, nil)
	if report.Superseded != 0 {
		t.Fatalf("PreserveVerbatim reads and distinct files must be untouched: %+v", report)
	}
}
