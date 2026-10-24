/*
 * ChatCLI - Adapter binding the @view tool to the session vision pipeline.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Implements plugins.ViewAdapter: loads a local image (same loader and size
 * caps as @file attachments), routes it through gateImagesForModel (native
 * vision with compression, describe-fallback, or off) and stages the result
 * on the live agent loop, which attaches it to the conversation at the next
 * turn boundary — never between a tool_use and its tool_result.
 */
package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/diillson/chatcli/models"
)

// Model-facing result strings (named per house style — the adapter speaks to
// the LLM, not the user, so these stay English like every other tool result).
const (
	viewMsgStagedFmt    = "Image %s staged — it will be attached to the conversation on the next turn; analyze what is visible then."
	viewMsgDescribedFmt = "The current model has no native vision; text description of %s:\n%s"
	viewMsgPDFStagedFmt = "PDF %s staged (%s) — it will be attached to the conversation on the next turn; read it then."
	viewMsgPDFTextFmt   = "Text of PDF %s (%s):\n%s"
	viewMsgPDFUnreadFmt = "%q is not a readable PDF within the %d MB limit"
	viewMsgNotImageFmt  = "%q is not a readable png/jpeg/gif/webp image within the attachment size limit"
	viewMsgNoLoop       = "no agent loop is running to attach the image to — in chat mode use @file instead"
	viewMsgDisabled     = "vision input is disabled (CHATCLI_VISION_INPUT=off): the image was not attached"
	viewMsgNoDescriber  = "the current model (%s) does not take image input and no vision model could describe the image (none reachable with this account). Tell the user: switch to a vision model, or set CHATCLI_VISION_PROVIDER and CHATCLI_VISION_MODEL to one they can use"
)

// viewToolAdapter is the concrete plugins.ViewAdapter.
type viewToolAdapter struct {
	cli *ChatCLI
}

// ViewImage implements plugins.ViewAdapter.
func (a *viewToolAdapter) ViewImage(ctx context.Context, path string) (string, error) {
	if doc, ok, err := a.cli.loadPDFAttachment(ctx, path); ok || strings.HasSuffix(strings.ToLower(strings.TrimSpace(path)), ".pdf") {
		if err != nil {
			return "", err
		}
		if !ok {
			return "", fmt.Errorf(viewMsgPDFUnreadFmt, path, maxPDFAttachmentBytes>>20)
		}
		return a.viewPDF(ctx, path, doc)
	}
	img, ok := a.cli.loadImageAttachment(path)
	if !ok {
		return "", fmt.Errorf(viewMsgNotImageFmt, path)
	}

	images, desc := a.cli.gateImagesForModel(ctx, []models.ImageContent{img})
	if len(images) > 0 {
		am := a.cli.agentMode
		if am == nil {
			return "", errors.New(viewMsgNoLoop)
		}
		am.stageViewedImage(images[0], path)
		return fmt.Sprintf(viewMsgStagedFmt, path), nil
	}
	if strings.TrimSpace(desc) != "" {
		return fmt.Sprintf(viewMsgDescribedFmt, path, desc), nil
	}
	if a.cli.resolveVisionModeCtx(ctx, a.cli.Provider, a.cli.Model, false) == visionOff {
		return "", errors.New(viewMsgDisabled)
	}
	return "", fmt.Errorf(viewMsgNoDescriber, a.cli.Model)
}

// viewPDF attaches a PDF to the running loop (native for models that read
// PDFs, extracted text for the rest — decided when the message is sent), or,
// with no loop to attach to, returns its extracted text.
func (a *viewToolAdapter) viewPDF(ctx context.Context, path string, doc models.DocumentContent) (string, error) {
	docs := a.cli.gateDocumentsForRoute(ctx, a.cli.Provider, a.cli.Model, []models.DocumentContent{doc})
	if len(docs) == 0 {
		return "", errors.New(viewMsgDisabled)
	}
	pages := "unknown page count"
	if docs[0].Pages > 0 {
		pages = fmt.Sprintf("%d pages", docs[0].Pages)
	}
	if am := a.cli.agentMode; am != nil {
		am.stageViewedDocument(docs[0])
		return fmt.Sprintf(viewMsgPDFStagedFmt, path, pages), nil
	}
	if strings.TrimSpace(docs[0].Text) == "" {
		return "", fmt.Errorf(viewMsgPDFUnreadFmt, path, maxPDFAttachmentBytes>>20)
	}
	return fmt.Sprintf(viewMsgPDFTextFmt, path, pages, docs[0].Text), nil
}
