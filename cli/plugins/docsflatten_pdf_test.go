/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package plugins

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDocsFlatten_PDF(t *testing.T) {
	root := t.TempDir()
	data, err := os.ReadFile("../../pkg/pdftext/testdata/report.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "docs", "report-q3.pdf"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	chunks, summary := flattenFixture(t, `{"root":"`+filepath.ToSlash(root)+`","format":"jsonl","output":"OUT"}`)
	if len(chunks) != 1 || chunks[0].Source != "docs/report-q3.pdf" || chunks[0].Title != "report-q3" {
		t.Fatalf("chunks = %+v (%s)", chunks, summary)
	}
	c := chunks[0].Content
	for _, want := range []string{"PDF document: docs/report-q3.pdf (2 pages)", "## Page 1", "## Page 2", "Ação corretiva", "R$ 12.400,00"} {
		if !strings.Contains(c, want) {
			t.Errorf("chunk lacks %q:\n%s", want, c)
		}
	}
}
