/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package claudeai

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/diillson/chatcli/models"
)

func TestBuildClaudeToolMessages_DocumentBlock(t *testing.T) {
	t.Setenv("CHATCLI_PDF_INPUT", "")
	doc := models.DocumentContent{MediaType: models.PDFMediaType, Data: []byte("%PDF-1.7 x"), FileName: "contract.pdf", Text: "clause 1"}
	history := []models.Message{{Role: "user", Content: "Summarize", Documents: []models.DocumentContent{doc}}}
	b, _ := json.Marshal(buildClaudeToolMessages("Summarize", history, nil, "claude-sonnet-4-6"))
	if !strings.Contains(string(b), `"type":"document"`) || !strings.Contains(string(b), `"title":"contract.pdf"`) {
		t.Fatalf("document block missing: %s", b)
	}
}
