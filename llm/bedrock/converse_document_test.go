/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package bedrock

import (
	"strings"
	"testing"

	bedrockruntimetypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/diillson/chatcli/models"
)

func converseDoc(name string, size int) models.DocumentContent {
	return models.DocumentContent{MediaType: models.PDFMediaType, Data: append([]byte("%PDF-1.7\n"), make([]byte, size)...), FileName: name, Pages: 1, Text: "Invoice total: 42"}
}

// Nova reads PDFs natively on Converse: the document travels as a
// DocumentBlock with a sanitized name, next to a text block.
func TestConverse_NovaGetsDocumentBlock(t *testing.T) {
	t.Setenv("CHATCLI_PDF_INPUT", "")
	history := []models.Message{{Role: "user", Content: "", Documents: []models.DocumentContent{converseDoc("invoice.v2.pdf", 10)}}}
	msgs, _ := buildConverseMessages("", history, "us.amazon.nova-pro-v1:0")
	if len(msgs) != 1 {
		t.Fatalf("messages = %d", len(msgs))
	}
	var doc *bedrockruntimetypes.ContentBlockMemberDocument
	var text string
	for _, b := range msgs[0].Content {
		switch v := b.(type) {
		case *bedrockruntimetypes.ContentBlockMemberDocument:
			doc = v
		case *bedrockruntimetypes.ContentBlockMemberText:
			text = v.Value
		}
	}
	if doc == nil || *doc.Value.Name != "invoice-v2" || doc.Value.Format != bedrockruntimetypes.DocumentFormatPdf {
		t.Fatalf("document block = %+v", doc)
	}
	if strings.TrimSpace(text) == "" {
		t.Fatal("Converse needs a text block next to a document")
	}
}

// A Converse model without native PDF input, or a PDF over Converse's
// limits, gets the extracted text.
func TestConverse_TextFallback(t *testing.T) {
	t.Setenv("CHATCLI_PDF_INPUT", "")
	history := []models.Message{{Role: "user", Content: "Total?", Documents: []models.DocumentContent{converseDoc("invoice.pdf", 10)}}}
	msgs, _ := buildConverseMessages("", history, "meta.llama3-3-70b-instruct-v1:0")
	text := msgs[0].Content[len(msgs[0].Content)-1].(*bedrockruntimetypes.ContentBlockMemberText).Value
	if !strings.Contains(text, "Invoice total: 42") {
		t.Fatalf("text = %q", text)
	}
	for _, b := range msgs[0].Content {
		if _, isDoc := b.(*bedrockruntimetypes.ContentBlockMemberDocument); isDoc {
			t.Fatal("no document block for a model without native PDF input")
		}
	}
	big := []models.Message{{Role: "user", Content: "Total?", Documents: []models.DocumentContent{converseDoc("big.pdf", 5_000_000)}}}
	msgs, _ = buildConverseMessages("", big, "us.amazon.nova-pro-v1:0")
	if !strings.Contains(msgs[0].Content[len(msgs[0].Content)-1].(*bedrockruntimetypes.ContentBlockMemberText).Value, "Invoice total: 42") {
		t.Fatal("a PDF over 4.5 MB goes as text on Converse")
	}
}
