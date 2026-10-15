/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package models

import (
	"bytes"
	"strings"
)

// PDFMediaType is the media type of every document chatcli attaches.
const PDFMediaType = "application/pdf"

// DocumentContent is a document (a PDF) attached to a turn.
type DocumentContent struct {
	MediaType string `json:"media_type"`          // application/pdf
	Data      []byte `json:"data,omitempty"`      // raw bytes (NOT base64); adapters encode on the wire
	FileName  string `json:"file_name,omitempty"` // original file name, shown to the model and in history
	// Pages is the page count when known (0 = unknown).
	Pages int `json:"pages,omitempty"`
	// Text is the extracted text, filled when the document is read, so a
	// model without native PDF input (and the history compactor) can
	// still use it.
	Text string `json:"text,omitempty"`
}

// IsPDF reports whether data is a PDF file (it starts with the %PDF-
// header, possibly after a few bytes of junk the format tolerates).
func IsPDF(data []byte) bool {
	head := data
	if len(head) > 1024 {
		head = head[:1024]
	}
	return bytes.Contains(head, []byte("%PDF-"))
}

// IsValid reports whether the document can be sent: a PDF with bytes.
func (d DocumentContent) IsValid() bool {
	return strings.EqualFold(d.MediaType, PDFMediaType) && len(d.Data) > 0
}

// Name returns the file's base name (whatever path a client sent with it),
// or "document.pdf".
func (d DocumentContent) Name() string {
	n := strings.TrimSpace(strings.ReplaceAll(d.FileName, "\\", "/"))
	if i := strings.LastIndexByte(n, '/'); i >= 0 {
		n = n[i+1:]
	}
	if n == "" || n == "." || n == ".." {
		return "document.pdf"
	}
	return n
}

// EstimateDocumentTokens approximates the input tokens a document costs
// natively: providers bill each page as its text plus a page image, which
// lands around 1,500–3,000 tokens per page; without a page count the
// extracted text (or the byte size) is the best signal.
func EstimateDocumentTokens(d DocumentContent) int {
	if d.Pages > 0 {
		return d.Pages * 2000
	}
	if d.Text != "" {
		return len(d.Text)/4 + 1500
	}
	return len(d.Data)/40 + 1500
}
