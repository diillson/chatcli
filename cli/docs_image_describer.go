/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"

	"github.com/diillson/chatcli/llm/catalog"
	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/pkg/pulse"
)

// docsImageMaxTokens caps one image description: a dense architecture
// diagram's elements, flows and verbatim text fit comfortably.
const docsImageMaxTokens = 2000

// docsImageDescriber is the plugins.DocsImageDescriber @docs-flatten uses
// to describe the images a documentation corpus references.
type docsImageDescriber struct {
	cli *ChatCLI
}

// errNoVisionModel is returned when no configured provider has a vision model.
var errNoVisionModel = errors.New("no vision-capable model is configured (set CHATCLI_VISION_PROVIDER and CHATCLI_VISION_MODEL, or use a provider with a vision model)")

// DescribeImage implements plugins.DocsImageDescriber. The model is the
// active one when it has vision; otherwise the vision pipeline's own
// choice (CHATCLI_VISION_PROVIDER/MODEL, then a vision model on the current
// provider, then on any configured provider).
func (d *docsImageDescriber) DescribeImage(ctx context.Context, prompt string, img models.ImageContent) (string, string, error) {
	ctx, span := beginMediaTool(ctx, pulseDocsDescribe)
	span.With("bytes_in", strconv.Itoa(len(img.Data)))
	desc, by, err := d.describe(ctx, prompt, img, span)
	if err == nil {
		span.With("chars_out", strconv.Itoa(len(desc)))
	}
	span.EndErr(err)
	return desc, by, err
}

// describe is DescribeImage under its dashboard node; span learns the
// model chosen.
func (d *docsImageDescriber) describe(ctx context.Context, prompt string, img models.ImageContent, span *pulse.Span) (string, string, error) {
	provider, model, ok := d.pick()
	if !ok {
		return "", "", errNoVisionModel
	}
	span.With("provider", strings.ToLower(provider)).With("model", model)
	client, err := d.cli.manager.GetClient(provider, model)
	if err != nil {
		return "", "", err
	}
	images := d.cli.compressImagesForVision([]models.ImageContent{img})
	msg := models.Message{Role: "user", Content: prompt, Images: images}
	desc, err := client.SendPrompt(ctx, prompt, []models.Message{msg}, docsImageMaxTokens)
	if err != nil {
		return "", "", err
	}
	return strings.TrimSpace(desc), strings.ToLower(provider) + "/" + model, nil
}

// pick chooses the describing model, preferring the active one.
func (d *docsImageDescriber) pick() (string, string, bool) {
	c := d.cli
	if p, m := c.Provider, c.Model; p != "" && m != "" && !envVisionOverride() {
		if meta, ok := catalog.Resolve(p, m); ok && metaHasVision(meta) {
			return p, m, true
		}
		if modelIDImpliesVision(m) {
			return p, m, true
		}
	}
	return c.resolveVisionDescribeModel()
}

// envVisionOverride reports whether CHATCLI_VISION_PROVIDER/MODEL pin the
// describing model; the explicit choice wins over the active model.
func envVisionOverride() bool {
	return strings.TrimSpace(os.Getenv("CHATCLI_VISION_PROVIDER")) != "" && strings.TrimSpace(os.Getenv("CHATCLI_VISION_MODEL")) != ""
}
