/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package ollama

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

// Ollama has no document input: the PDF arrives as its extracted text.
func TestOllama_PDFAsText(t *testing.T) {
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		_, _ = io.WriteString(w, `{"message":{"role":"assistant","content":"ok"},"done":true}`)
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "llama3.3", zap.NewNop(), 1, 0)
	doc := models.DocumentContent{MediaType: models.PDFMediaType, Data: []byte("%PDF-1.7 x"), FileName: "manual.pdf", Pages: 3, Text: "Step 1: unplug the device."}
	if _, err := c.SendPrompt(context.Background(), "What is step 1?", []models.Message{{Role: "user", Content: "What is step 1?", Documents: []models.DocumentContent{doc}}}, 0); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "Step 1: unplug the device.") || !strings.Contains(body, "manual.pdf") || strings.Contains(body, "JVBERi") {
		t.Fatalf("request body = %s", body)
	}
}
