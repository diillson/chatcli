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

// viewToolAdapter is the concrete plugins.ViewAdapter.
type viewToolAdapter struct {
	cli *ChatCLI
}

// ViewImage implements plugins.ViewAdapter.
func (a *viewToolAdapter) ViewImage(ctx context.Context, path string) (string, error) {
	if strings.HasSuffix(strings.ToLower(strings.TrimSpace(path)), ".pdf") {
		return "", errors.New("PDFs are not supported yet — render the page to an image (png/jpeg) first")
	}
	img, ok := a.cli.loadImageAttachment(path)
	if !ok {
		return "", fmt.Errorf("%q is not a readable png/jpeg/gif/webp image within the attachment size limit", path)
	}

	images, desc := a.cli.gateImagesForModel(ctx, []models.ImageContent{img})
	if len(images) > 0 {
		am := a.cli.agentMode
		if am == nil {
			return "", errors.New("no agent loop is running to attach the image to — in chat mode use @file instead")
		}
		am.stageViewedImage(images[0], path)
		return fmt.Sprintf("Image %s staged — it will be attached to the conversation on the next turn; analyze what is visible then.", path), nil
	}
	if strings.TrimSpace(desc) != "" {
		return fmt.Sprintf("The current model has no native vision; text description of %s:\n%s", path, desc), nil
	}
	return "", errors.New("vision input is disabled or unavailable for the current model (see CHATCLI_VISION_INPUT)")
}
