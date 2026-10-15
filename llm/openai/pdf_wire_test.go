/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package openai

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/diillson/chatcli/models"
)

func TestBuildToolMessages_PDFNativeOnVisionModels(t *testing.T) {
	t.Setenv("CHATCLI_PDF_INPUT", "")
	doc := models.DocumentContent{MediaType: models.PDFMediaType, Data: []byte("%PDF-1.7 x"), FileName: "a.pdf", Text: "page text"}
	history := []models.Message{{Role: "user", Content: "Read it", Documents: []models.DocumentContent{doc}}}
	b, _ := json.Marshal(buildToolMessages("Read it", history, "gpt-5.5"))
	if !strings.Contains(string(b), `"type":"file"`) || strings.Contains(string(b), "page text") {
		t.Fatalf("gpt-5.5 must get the file part: %s", b)
	}
	b, _ = json.Marshal(buildToolMessages("Read it", history, "gpt-3.5-turbo"))
	if strings.Contains(string(b), `"type":"file"`) || !strings.Contains(string(b), "page text") {
		t.Fatalf("a model without PDF input gets the text: %s", b)
	}
}
