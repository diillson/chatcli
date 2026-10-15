/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package rpcserve

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// An editor's embedded PDF resource and a resource_link to a local PDF both
// reach the model as the document's text.
func TestFlattenPromptParts_PDF(t *testing.T) {
	data, err := os.ReadFile("../../pkg/pdftext/testdata/report.pdf")
	if err != nil {
		t.Fatal(err)
	}
	local := filepath.Join(t.TempDir(), "spec.pdf")
	if err := os.WriteFile(local, data, 0o600); err != nil {
		t.Fatal(err)
	}
	raw := `{"sessionId":"s","prompt":[
		{"type":"text","text":"Compare"},
		{"type":"resource","resource":{"uri":"file:///x/report.pdf","mimeType":"application/pdf","blob":"` + base64.StdEncoding.EncodeToString(data) + `"}},
		{"type":"resource_link","uri":"file://` + filepath.ToSlash(local) + `","name":"spec.pdf"}]}`
	var p acpPromptParams
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		t.Fatal(err)
	}
	out := flattenPromptParts(context.Background(), p)
	if strings.Count(out, "Ação corretiva") != 2 || !strings.Contains(out, "Attached PDF (report.pdf, 2 pages)") ||
		!strings.Contains(out, "Attached PDF (spec.pdf, 2 pages)") || strings.Contains(out, "Referenced file") {
		t.Fatalf("out = %.500s", out)
	}
}
