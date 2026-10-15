package ctxmgr

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diillson/chatcli/utils"
	"go.uber.org/zap"
)

// Diagram and image chunks are Markdown whatever their source extension.
func TestKnowledgeJSONL_KindTypesChunksAsMarkdown(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.jsonl")
	lines := `{"id":"img/arch.png#0001","source":"img/arch.png","content":"# Image: img/arch.png\n\nA → B","kind":"image"}
{"id":"docs/a.md#0002","source":"docs/a.md","content":"# A\n\ntext"}
`
	if err := os.WriteFile(p, []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}
	files, _, err := ingestKnowledgeJSONL(p, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	if files[0].Type != "md" || files[1].Type != "md" {
		t.Fatalf("types = %q, %q", files[0].Type, files[1].Type)
	}
}

// A "#" line inside a fence (a dot comment, a shell comment) is not a
// heading: the segmenter never cuts there, so a diagram is not split
// between two passages.
func TestSegment_NoCutInsideMarkdownFence(t *testing.T) {
	// The window ends inside a small dot diagram whose "#" comments the old
	// boundary scorer took for headings; the cut must land before the
	// fence (the blank line) instead.
	var b strings.Builder
	b.WriteString("# Title\n")
	for i := 0; i < 5; i++ {
		b.WriteString("prose line that fills the window up\n")
	}
	b.WriteString("\n```dot\ndigraph G {\n# routing comment\n  a -> b\n# another comment\n  b -> c\n}\n```\n")
	for i := 0; i < 10; i++ {
		b.WriteString("prose after the diagram, line\n")
	}
	f := utils.FileInfo{Path: "docs/x.md", Content: b.String(), Type: "md"}
	segs := segmentOne(f, SegmentOptions{MaxChars: 260, Boundaries: true})
	for _, s := range segs {
		first := strings.TrimSpace(strings.SplitN(s.Content, "\n", 2)[0])
		if strings.HasPrefix(first, "# routing") || strings.HasPrefix(first, "# another") {
			t.Fatalf("a segment starts inside the fence:\n%s", s.Content)
		}
	}
	if markdownFencedLines([]string{"x"}, "go") != nil {
		t.Fatal("fence map only for Markdown")
	}
}
