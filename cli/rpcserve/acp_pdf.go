/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package rpcserve

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"github.com/diillson/chatcli/pkg/atrest"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/diillson/chatcli/pkg/pdftext"
	"github.com/diillson/chatcli/utils"
)

// Model-facing renderings of an attached PDF (English, like every prompt
// part the engine writes).
const (
	acpPDFUnreadableFmt  = "Attached PDF %s could not be read: %v"
	acpPDFTextFmt        = "Attached PDF (%s, %d pages):\n%s"
	acpPDFScannedFmt     = "Attached PDF %s (%d pages) is a scanned document with no text layer."
	acpPDFScannedViewFmt = "Attached PDF %s (%d pages) is a scanned document with no text layer; open it with @view %s to read its pages."
)

// acpMaxPDFBytes caps a PDF an ACP client hands over.
const acpMaxPDFBytes = 50 << 20

// acpPDFFromPath renders a PDF the client referenced by path: its extracted
// text, so the model reads it in any mode (a chat turn has no tools to open
// it). ok is false when the path is not a PDF.
func acpPDFFromPath(ctx context.Context, path string) (string, bool) {
	if !strings.EqualFold(filepath.Ext(path), ".pdf") {
		return "", false
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() || info.Size() > acpMaxPDFBytes {
		return "", false
	}
	data, err := atrest.ReadFile(path)
	if err != nil {
		return "", false
	}
	return acpPDFText(ctx, filepath.Base(path), path, data)
}

// acpPDFFromBlob renders an embedded PDF resource. A scanned PDF is saved
// to a private file so an agent turn can open it with @view.
func acpPDFFromBlob(ctx context.Context, name, mime, blob string) (string, bool) {
	data, err := base64.StdEncoding.DecodeString(strings.TrimSpace(blob))
	if err != nil || len(data) == 0 || len(data) > acpMaxPDFBytes {
		return "", false
	}
	if !strings.EqualFold(mime, "application/pdf") && !bytes.Contains(data[:min(len(data), 1024)], []byte("%PDF-")) {
		return "", false
	}
	if name == "" {
		name = "document.pdf"
	}
	return acpPDFText(ctx, filepath.Base(name), "", data)
}

func acpPDFText(ctx context.Context, name, path string, data []byte) (string, bool) {
	res, err := pdftext.Extract(ctx, data, pdftext.Limits{Timeout: time.Minute})
	if err != nil {
		return fmt.Sprintf(acpPDFUnreadableFmt, name, err), true
	}
	if !res.Scanned {
		return fmt.Sprintf(acpPDFTextFmt, name, res.TotalPages, res.Text()), true
	}
	if path == "" {
		if dir, derr := utils.PrivateStateDir("attachments"); derr == nil {
			if f, ferr := os.CreateTemp(dir, "acp-*.pdf"); ferr == nil {
				// Sealed at rest like every other attachment; @view opens it.
				sealed, serr := atrest.SealAt(f.Name(), data)
				if serr == nil {
					_, serr = f.Write(sealed)
				}
				_ = f.Close()
				if serr == nil {
					path = f.Name()
				} else {
					_ = os.Remove(f.Name())
				}
			}
		}
	}
	if path == "" {
		return fmt.Sprintf(acpPDFScannedFmt, name, res.TotalPages), true
	}
	return fmt.Sprintf(acpPDFScannedViewFmt, name, res.TotalPages, path), true
}
