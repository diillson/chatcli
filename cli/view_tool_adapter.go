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
	viewMsgPDF          = "PDFs are not supported yet — render the page to an image (png/jpeg) first"
	viewMsgNotImageFmt  = "%q is not a readable png/jpeg/gif/webp image within the attachment size limit"
	viewMsgNoLoop       = "no agent loop is running to attach the image to — in chat mode use @file instead"
	viewMsgDisabled     = "vision input is disabled or unavailable for the current model (see CHATCLI_VISION_INPUT)"
)

// viewToolAdapter is the concrete plugins.ViewAdapter.
type viewToolAdapter struct {
	cli *ChatCLI
}

// ViewImage implements plugins.ViewAdapter.
func (a *viewToolAdapter) ViewImage(ctx context.Context, path string) (string, error) {
	if strings.HasSuffix(strings.ToLower(strings.TrimSpace(path)), ".pdf") {
		return "", errors.New(viewMsgPDF)
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
	return "", errors.New(viewMsgDisabled)
}
