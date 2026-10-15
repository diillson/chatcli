/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package visionwire

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/diillson/chatcli/llm/catalog"
	"github.com/diillson/chatcli/models"
)

func pdfDoc(name, text string, size int) models.DocumentContent {
	data := append([]byte("%PDF-1.7\n"), make([]byte, size)...)
	return models.DocumentContent{MediaType: models.PDFMediaType, Data: data, FileName: name, Pages: 2, Text: text}
}

func marshal(t *testing.T, c Content) string {
	t.Helper()
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Every provider gets the PDF: natively where the model reads PDFs, as its
// extracted text everywhere else.
func TestSplitDocuments_PerProvider(t *testing.T) {
	t.Setenv("CHATCLI_PDF_INPUT", "")
	doc := pdfDoc("report.pdf", "--- page 1 ---\nQuarterly revenue grew 12%.", 100)
	cases := []struct {
		provider, model string
		native          bool
	}{
		{catalog.ProviderClaudeAI, "claude-sonnet-4-6", true},
		{catalog.ProviderOpenAI, "gpt-5.5", true},
		{catalog.ProviderGoogleAI, "gemini-3.5-flash", true},
		{catalog.ProviderOpenRouter, "anthropic/claude-sonnet-4.6", true},
		{catalog.ProviderBedrock, "us.anthropic.claude-sonnet-4-6", true},
		{catalog.ProviderBedrock, "us.amazon.nova-pro-v1:0", true},
		{catalog.ProviderBedrock, "us.amazon.nova-micro-v1:0", false},
		{catalog.ProviderBedrock, "openai.gpt-oss-120b-1:0", false},
		{catalog.ProviderZAI, "glm-5.3-flash", true},
		{catalog.ProviderZAI, "glm-5.3", false},
		{catalog.ProviderXAI, "grok-4.20", false},
		{catalog.ProviderMoonshot, "kimi-k3", false},
		{catalog.ProviderMiniMax, "minimax-m3", false},
		{catalog.ProviderOllama, "llama3.3", false},
		{catalog.ProviderCopilot, "gpt-5.5", false},
		{"", "", false}, // text-only clients
	}
	for _, c := range cases {
		text, native := SplitDocuments(c.provider, c.model, "Summarize it.", []models.DocumentContent{doc})
		if got := len(native) == 1; got != c.native {
			t.Errorf("%s/%s: native=%v, want %v", c.provider, c.model, got, c.native)
		}
		hasText := strings.Contains(text, "Quarterly revenue grew 12%") && strings.Contains(text, "[PDF report.pdf — 2 pages — extracted text]")
		if hasText == c.native {
			t.Errorf("%s/%s: extracted text folded=%v, want %v", c.provider, c.model, hasText, !c.native)
		}
	}
}

func TestSplitDocuments_LimitsAndOverride(t *testing.T) {
	t.Setenv("CHATCLI_PDF_INPUT", "")
	big := pdfDoc("big.pdf", "text", 33<<20)
	if _, native := SplitDocuments(catalog.ProviderClaudeAI, "claude-sonnet-4-6", "", []models.DocumentContent{big}); len(native) != 0 {
		t.Error("a PDF over Anthropic's 32 MB goes as text")
	}
	long := pdfDoc("long.pdf", "text", 10)
	long.Pages = 400
	if _, native := SplitDocuments(catalog.ProviderOpenAI, "gpt-5.5", "", []models.DocumentContent{long}); len(native) != 0 {
		t.Error("a PDF over the native page cap goes as text")
	}
	t.Setenv("CHATCLI_PDF_INPUT", "text")
	if _, native := SplitDocuments(catalog.ProviderClaudeAI, "claude-sonnet-4-6", "", []models.DocumentContent{pdfDoc("a.pdf", "t", 1)}); len(native) != 0 {
		t.Error("CHATCLI_PDF_INPUT=text forces text")
	}
	t.Setenv("CHATCLI_PDF_INPUT", "native")
	if _, native := SplitDocuments(catalog.ProviderXAI, "grok-4.20", "", []models.DocumentContent{pdfDoc("a.pdf", "t", 1)}); len(native) != 1 {
		t.Error("CHATCLI_PDF_INPUT=native forces blocks")
	}
}

// A scanned PDF (no text) bound for a text-only model says so instead of
// vanishing.
func TestDocumentsAsText_Scanned(t *testing.T) {
	out := DocumentsAsText([]models.DocumentContent{pdfDoc("scan.pdf", "", 1)})
	if !strings.Contains(out, "scan.pdf") || !strings.Contains(out, "no extractable text") {
		t.Fatalf("scanned note = %q", out)
	}
}

func TestBuilders_EmitNativeDocumentShapes(t *testing.T) {
	t.Setenv("CHATCLI_PDF_INPUT", "")
	msg := models.Message{Role: "user", Content: "What does it say?", Documents: []models.DocumentContent{pdfDoc("q3 report.pdf", "t", 4)}}

	openai := marshal(t, OpenAIMessageContent(catalog.ProviderOpenAI, "gpt-5.5", msg))
	if !strings.Contains(openai, `"type":"file"`) || !strings.Contains(openai, `"file_data":"data:application/pdf;base64,`) || !strings.Contains(openai, `"filename":"q3 report.pdf"`) {
		t.Errorf("OpenAI shape: %s", openai)
	}
	anth := marshal(t, AnthropicMessageContent(catalog.ProviderClaudeAI, "claude-sonnet-4-6", msg))
	if !strings.Contains(anth, `"type":"document"`) || !strings.Contains(anth, `"media_type":"application/pdf"`) || strings.Index(anth, `"document"`) > strings.Index(anth, `"text":"What does it say?"`) {
		t.Errorf("Anthropic shape (document first): %s", anth)
	}
	oauth := marshal(t, AnthropicOAuthMessageContent(catalog.ProviderClaudeAI, "claude-sonnet-4-6", msg))
	if !strings.Contains(oauth, `"type":"document"`) {
		t.Errorf("Anthropic OAuth shape: %s", oauth)
	}
	resp := marshal(t, ResponsesMessageContent(catalog.ProviderOpenAI, "gpt-5.5", msg))
	if !strings.Contains(resp, `"type":"input_file"`) || !strings.Contains(resp, `"file_data":"data:application/pdf;base64,`) {
		t.Errorf("Responses shape: %s", resp)
	}
	gem := marshal(t, GeminiMessageParts(catalog.ProviderGoogleAI, "gemini-3.5-flash", msg))
	if !strings.Contains(gem, `"mime_type":"application/pdf"`) {
		t.Errorf("Gemini shape: %s", gem)
	}
	// A text-only provider gets a plain string with the text folded in.
	xai := marshal(t, OpenAIMessageContent(catalog.ProviderXAI, "grok-4.20", msg))
	if strings.Contains(xai, `"type":"file"`) || !strings.HasPrefix(xai, `"What does it say?`) {
		t.Errorf("xAI must get text: %s", xai)
	}
	// No documents: byte-identical to the image-only builders.
	plain := models.Message{Role: "user", Content: "hi"}
	if marshal(t, OpenAIMessageContent(catalog.ProviderOpenAI, "gpt-5.5", plain)) != `"hi"` {
		t.Error("text-only message changed shape")
	}
}

func TestBedrockDocumentName(t *testing.T) {
	cases := map[string]string{
		"report.pdf":             "report",
		"Q3 Report (final).pdf":  "Q3 Report (final)",
		"contrato_2026.v2.pdf":   "contrato-2026-v2",
		"  ../../etc/passwd  ":   "etc-passwd",
		"relatório   final.PDF":  "relat-rio final",
		"":                       "document",
		strings.Repeat("a", 300): strings.Repeat("a", 200),
	}
	for in, want := range cases {
		if got := BedrockDocumentName(in); got != want {
			t.Errorf("BedrockDocumentName(%q) = %q, want %q", in, got, want)
		}
	}
}
