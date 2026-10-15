/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package plugins

import (
	"context"
	"fmt"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/diillson/chatcli/pkg/pdftext"
)

// Model-facing messages (English, like every tool result).
const (
	pdfErrReadFmt    = "PDF: %w"
	pdfErrScannedFmt = "PDF with no extractable text (%d scanned pages): attach it with @view to a model that reads PDFs natively"
)

// pdfPageMarker is pdftext's page separator.
var pdfPageMarker = regexp.MustCompile(`(?m)^--- page (\d+) ---$`)

// pdfPieces turns a PDF into chunks of its extracted text. Pages become
// "## Page N" sections, so chunks break between pages and a retrieval hit
// says which page it came from. A scanned PDF (no text layer) is reported,
// not silently dropped.
func pdfPieces(ctx context.Context, data []byte, relPath string, maxChars int) ([]flatChunk, error) {
	res, err := pdftext.Extract(ctx, data, pdftext.Limits{Timeout: 2 * time.Minute})
	if err != nil {
		return nil, fmt.Errorf(pdfErrReadFmt, err)
	}
	if res.Scanned {
		return nil, fmt.Errorf(pdfErrScannedFmt, res.TotalPages)
	}
	title := strings.TrimSuffix(path.Base(relPath), path.Ext(relPath))
	body := pdfPageMarker.ReplaceAllString(res.Text(), "## Page $1")
	content := fmt.Sprintf("# %s\n\nPDF document: %s (%d pages)\n\n%s", title, relPath, res.TotalPages, body)
	parts := chunkMarkdown(content, maxChars)
	pieces := make([]flatChunk, 0, len(parts))
	for _, c := range parts {
		pieces = append(pieces, flatChunk{Content: c, Title: title})
	}
	return pieces, nil
}

// defaultDocsFlattenMaxPDFSize is the default cap for a PDF: its bytes are
// mostly fonts and images, so the 1 MiB text-file default would skip
// nearly every real document.
const defaultDocsFlattenMaxPDFSize = 50 << 20

// docsFlattenFileCap is the size cap for one file: an explicit maxSize
// applies to every file; otherwise PDFs get their own default.
func docsFlattenFileCap(cfg docsFlattenArgs, name string) int64 {
	if cfg.MaxSize <= 0 && strings.EqualFold(path.Ext(name), ".pdf") {
		return defaultDocsFlattenMaxPDFSize
	}
	return docsFlattenMaxFileSize(cfg)
}
