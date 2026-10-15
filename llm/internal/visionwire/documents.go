/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * PDF documents on the wire.
 *
 * A turn can carry PDFs (models.Message.Documents). Whether a PDF goes out
 * as a native document block or as its extracted text is decided here, per
 * provider and model, at the moment the message is serialized — not when the
 * file was attached. A conversation that attached a PDF on Claude and then
 * moves to a model without native PDF input (a /switch, a failover, a
 * sub-agent or summarizer on another model) therefore still sends the
 * document, as text, instead of a block the API rejects.
 */
package visionwire

import (
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/diillson/chatcli/llm/catalog"
	"github.com/diillson/chatcli/models"
)

// Per-provider native limits (bytes per document), from each provider's
// documentation. A document over its provider's limit is sent as text.
var nativePDFMaxBytes = map[string]int{
	catalog.ProviderClaudeAI:   32 << 20,
	catalog.ProviderOpenAI:     50 << 20,
	catalog.ProviderGoogleAI:   50 << 20,
	catalog.ProviderOpenRouter: 50 << 20,
	catalog.ProviderZAI:        50 << 20,
	catalog.ProviderBedrock:    20 << 20, // InvokeModel payload; Converse is lower (bedrockConverseMaxPDF)
}

// nativePDFMaxPages caps the pages sent natively (Anthropic: 100 pages per
// request below a 1M context window). Larger documents go as text.
const nativePDFMaxPages = 100

// NativePDF reports whether documents go to provider/model as native
// blocks (catalog.NativePDFInput: the catalog rule plus CHATCLI_PDF_INPUT).
func NativePDF(provider, model string) bool {
	return catalog.NativePDFInput(provider, model)
}

// SplitDocuments decides, per document, how a turn's PDFs reach
// provider/model: the ones it reads natively are returned for the caller's
// document blocks; the rest are folded into the text as their extracted
// content. A provider of "" folds everything (clients without document
// blocks).
func SplitDocuments(provider, model, text string, docs []models.DocumentContent) (string, []models.DocumentContent) {
	if len(docs) == 0 {
		return text, nil
	}
	native := provider != "" && NativePDF(provider, model)
	maxBytes := nativePDFMaxBytes[strings.ToUpper(provider)]
	var keep []models.DocumentContent
	var folded []models.DocumentContent
	for _, d := range docs {
		switch {
		case !native || !d.IsValid():
			folded = append(folded, d)
		case maxBytes > 0 && len(d.Data) > maxBytes, d.Pages > nativePDFMaxPages:
			folded = append(folded, d)
		default:
			keep = append(keep, d)
		}
	}
	return text + DocumentsAsText(folded), keep
}

// DocumentsAsText renders documents as their extracted text, delimited so
// the model knows where each one starts and ends.
func DocumentsAsText(docs []models.DocumentContent) string {
	var b strings.Builder
	for _, d := range docs {
		pages := ""
		if d.Pages > 0 {
			pages = fmt.Sprintf(" — %d pages", d.Pages)
		}
		text := strings.TrimSpace(d.Text)
		if text == "" {
			fmt.Fprintf(&b, "\n\n[PDF %s%s: no extractable text (a scanned document?) and this model cannot read its pages. You do NOT know what it contains: do not guess or describe it — tell the user it could not be read and that a model with native PDF input (Claude, GPT-4o or later, Gemini) can read it.]", d.Name(), pages)
			continue
		}
		fmt.Fprintf(&b, "\n\n[PDF %s%s — extracted text]\n%s\n[end of PDF %s]", d.Name(), pages, text, d.Name())
	}
	return b.String()
}

// pdfBase64 is the document's bytes for the wire.
func pdfBase64(d models.DocumentContent) string {
	return base64.StdEncoding.EncodeToString(d.Data)
}

// openAIFilePart is the Chat Completions file content part (OpenAI,
// OpenRouter, Z.AI).
func openAIFilePart(d models.DocumentContent) map[string]interface{} {
	return map[string]interface{}{
		"type": "file",
		"file": map[string]interface{}{
			"filename":  d.Name(),
			"file_data": "data:" + models.PDFMediaType + ";base64," + pdfBase64(d),
		},
	}
}

// anthropicDocumentBlock is the Anthropic Messages document block (also on
// Bedrock InvokeModel). The title helps the model refer to the file.
func anthropicDocumentBlock(d models.DocumentContent) map[string]interface{} {
	return map[string]interface{}{
		"type":  "document",
		"title": d.Name(),
		"source": map[string]interface{}{
			"type":       "base64",
			"media_type": models.PDFMediaType,
			"data":       pdfBase64(d),
		},
	}
}

// responsesFilePart is the Responses API input_file item.
func responsesFilePart(d models.DocumentContent) map[string]interface{} {
	return map[string]interface{}{
		"type":      "input_file",
		"filename":  d.Name(),
		"file_data": "data:" + models.PDFMediaType + ";base64," + pdfBase64(d),
	}
}

// geminiPDFPart is the Gemini inline_data part for a PDF.
func geminiPDFPart(d models.DocumentContent) map[string]interface{} {
	return map[string]interface{}{
		"inline_data": map[string]interface{}{
			"mime_type": models.PDFMediaType,
			"data":      pdfBase64(d),
		},
	}
}

// BedrockDocumentName turns a file name into a Bedrock Converse document
// name: letters, digits, single spaces, hyphens, parentheses and square
// brackets only (no dots — "report.pdf" is rejected), 1–200 characters.
func BedrockDocumentName(name string) string {
	name = strings.TrimSuffix(name, ".pdf")
	name = strings.TrimSuffix(name, ".PDF")
	var b strings.Builder
	space := false
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '(', r == ')', r == '[', r == ']':
			b.WriteRune(r)
			space = false
		case r == ' ':
			if !space && b.Len() > 0 {
				b.WriteRune(' ')
			}
			space = true
		default:
			if !space && b.Len() > 0 {
				b.WriteRune('-')
			}
		}
	}
	out := strings.Trim(b.String(), " -")
	if out == "" {
		out = "document"
	}
	if len(out) > 200 {
		out = out[:200]
	}
	return out
}

// OpenAIMessageContent is OpenAIContent for a history message sent to
// provider/model: its PDFs go natively when the model reads them, as text
// otherwise.
func OpenAIMessageContent(provider, model string, msg models.Message) Content {
	text, docs := SplitDocuments(provider, model, msg.Content, msg.Documents)
	return openAIContentWithDocs(text, msg.Images, docs)
}

// AnthropicMessageContent is AnthropicContent for a history message.
func AnthropicMessageContent(provider, model string, msg models.Message) Content {
	text, docs := SplitDocuments(provider, model, msg.Content, msg.Documents)
	return anthropicContentWithDocs(text, msg.Images, docs)
}

// AnthropicOAuthMessageContent is AnthropicOAuthContent for a history message.
func AnthropicOAuthMessageContent(provider, model string, msg models.Message) Content {
	text, docs := SplitDocuments(provider, model, msg.Content, msg.Documents)
	return anthropicOAuthContentWithDocs(text, msg.Images, docs)
}

// ResponsesMessageContent is ResponsesUserContent for a history message.
func ResponsesMessageContent(provider, model string, msg models.Message) Content {
	text, docs := SplitDocuments(provider, model, msg.Content, msg.Documents)
	return responsesUserContentWithDocs(text, msg.Images, docs)
}

// GeminiMessageParts is GeminiParts for a history message.
func GeminiMessageParts(provider, model string, msg models.Message) Content {
	text, docs := SplitDocuments(provider, model, msg.Content, msg.Documents)
	return geminiPartsWithDocs(text, msg.Images, docs)
}

// TextWithDocuments is a message's text with its PDFs folded in as
// extracted text, for clients that send text only (no document blocks).
func TextWithDocuments(msg models.Message) string {
	text, _ := SplitDocuments("", "", msg.Content, msg.Documents)
	return text
}
