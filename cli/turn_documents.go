/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/diillson/chatcli/models"
)

// Model-facing text of the document note.
const (
	documentNoteHeader = "[Attached PDFs — the user attached these to this message. A copy of each is saved on disk (read one again with @read <path>):"
)

// saveTurnDocuments writes each PDF that arrived with a turn (web UI,
// channels, RPC) to the attachments store, like saveTurnAttachments does for
// images, and returns the note naming the copies. Best effort.
func (cli *ChatCLI) saveTurnDocuments(docs []models.DocumentContent) string {
	if len(docs) == 0 {
		return ""
	}
	dir := cli.attachmentsDir()
	if dir == "" {
		return ""
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		cli.logAttachmentError("creating the attachments store", err)
		return ""
	}
	stamp := time.Now().Format("20060102-150405")
	var lines []string
	for _, d := range docs {
		if !d.IsValid() || len(d.Data) > maxPDFAttachmentBytes {
			continue
		}
		path, err := writeAttachmentBytes(dir, stamp, d.Data, d.FileName, ".pdf")
		if err != nil {
			cli.logAttachmentError("saving a document", err)
			continue
		}
		lines = append(lines, fmt.Sprintf(attachmentNoteLineFmt, d.Name(), path))
	}
	if len(lines) == 0 {
		return ""
	}
	return "\n\n" + documentNoteHeader + "\n" + strings.Join(lines, "\n") + "\n" + attachmentNoteFooter
}
