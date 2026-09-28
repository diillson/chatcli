/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Saved turn attachments. An image a remote surface (the web UI) attaches to
 * a turn travels inline to the model, but inline bytes are all it had: no
 * file existed, so a coder or agent run that wanted to look again with @view
 * (or any tool that takes a path) had nothing to open, and a model whose
 * vision the turn could not use was never even told an image was there.
 * Each attachment is now also written to the attachments store under the
 * state root, and the turn carries a note naming the saved copies, so the
 * reference survives in the conversation — across turns, compaction and the
 * terminal continuing the session.
 */
package cli

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/pkg/atrest"
	"go.uber.org/zap"
)

// attachmentsDirName is the store the saved attachments live in, under the
// state root (~/.chatcli, or a gateway tenant's root).
const attachmentsDirName = "attachments"

// Model-facing text of the attachment note (English, like every other
// model-facing string the engine writes).
const (
	attachmentNoteHeader  = "[Attached images — the user attached these to this message. A copy of each is saved on disk for tools that take a file path (open one with @view <path> to look at it again):"
	attachmentNoteLineFmt = "- %s: %s"
	attachmentNoteFooter  = "]"
)

// attachmentNameMax bounds the part of a saved file's name taken from the
// name the user gave it.
const attachmentNameMax = 48

// attachmentExt maps a canonical image media type to the extension the
// saved copy gets. The name the user gave never picks the extension.
var attachmentExt = map[string]string{
	"image/png":  ".png",
	"image/jpeg": ".jpg",
	"image/gif":  ".gif",
	"image/webp": ".webp",
}

// attachmentsDir is where saved attachments go; "" when no state root is
// resolvable.
func (cli *ChatCLI) attachmentsDir() string {
	root := cli.storageRoot()
	if root == "" {
		return ""
	}
	return filepath.Join(root, attachmentsDirName)
}

// saveTurnAttachments writes each inline image to the attachments store and
// returns the note that tells the model where the copies are. Best effort:
// an image that cannot be saved (no bytes, over the attachment size cap,
// an unwritable store) is simply left out of the note, and no image saved
// means no note. With encryption at rest on, the copy is sealed like every
// other store; the file tools open it transparently.
func (cli *ChatCLI) saveTurnAttachments(images []models.ImageContent) string {
	if len(images) == 0 {
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
	for _, img := range images {
		path, err := writeAttachment(dir, stamp, img)
		if err != nil {
			cli.logAttachmentError("saving an attachment", err)
			continue
		}
		if path == "" {
			continue
		}
		lines = append(lines, fmt.Sprintf(attachmentNoteLineFmt, attachmentDisplayName(img), path))
	}
	if len(lines) == 0 {
		return ""
	}
	return "\n\n" + attachmentNoteHeader + "\n" + strings.Join(lines, "\n") + "\n" + attachmentNoteFooter
}

func (cli *ChatCLI) logAttachmentError(what string, err error) {
	if cli.logger != nil {
		cli.logger.Warn("attachments: "+what, zap.Error(err))
	}
}

// writeAttachment saves one image as a new file (never over an existing
// one) readable by the owner only. It returns "" without an error for an
// image there is nothing to save for.
func writeAttachment(dir, stamp string, img models.ImageContent) (string, error) {
	if len(img.Data) == 0 || len(img.Data) > maxImageAttachmentBytes {
		return "", nil
	}
	mime, ok := models.NormalizeImageMediaType(img.MediaType)
	if !ok {
		if mime, ok = models.DetectImageMediaType(img.Data); !ok {
			return "", nil
		}
	}
	var rnd [4]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return "", err
	}
	name := stamp + "-" + hex.EncodeToString(rnd[:]) + "-" + attachmentStem(img.FileName) + attachmentExt[mime]
	path := filepath.Join(dir, name)
	data, err := atrest.SealAt(path, img.Data)
	if err != nil {
		return "", err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 -- the path is the attachments store joined with a name built here from a timestamp, random hex and a sanitized stem
	if err != nil {
		return "", err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return "", err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return path, nil
}

// attachmentStem reduces a user-given file name to a safe stem: the base
// name without its extension, letters, digits, dot, dash and underscore
// only, bounded in length. An empty result becomes "image".
func attachmentStem(name string) string {
	base := filepath.Base(strings.ReplaceAll(strings.TrimSpace(name), "\\", "/"))
	base = strings.TrimSuffix(base, filepath.Ext(base))
	var b strings.Builder
	for _, r := range base {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		case r == '.' || r == ' ':
			b.WriteByte('_')
		}
		if b.Len() >= attachmentNameMax {
			break
		}
	}
	stem := strings.Trim(b.String(), "_-")
	if stem == "" {
		return "image"
	}
	return stem
}

// attachmentDisplayName is how the note names an image: the name the user
// gave it, or a generic label.
func attachmentDisplayName(img models.ImageContent) string {
	if n := strings.TrimSpace(filepath.Base(strings.ReplaceAll(img.FileName, "\\", "/"))); n != "" && n != "." && n != "/" {
		return n
	}
	return "image"
}

// withoutAttachmentNote returns content without the attachment note, for
// the places that summarize what the user wrote (the session title).
func withoutAttachmentNote(content string) string {
	if i := strings.Index(content, attachmentNoteHeader); i >= 0 {
		return strings.TrimSpace(content[:i])
	}
	return content
}

// takePendingInbound merges the images staged from outside the @file flow
// (the web UI, the gateway) into a loop run's images and returns the note
// about their saved copies, clearing both: they belong to one run.
func (cli *ChatCLI) takePendingInbound(images []models.ImageContent) ([]models.ImageContent, string) {
	if len(cli.pendingInboundImages) > 0 {
		images = append(images, cli.pendingInboundImages...)
		cli.pendingInboundImages = nil
	}
	note := cli.pendingInboundNote
	cli.pendingInboundNote = ""
	return images, note
}
