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

	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/pkg/pulse"
)

// docsImageDescriber is the plugins.DocsImageDescriber @docs-flatten uses
// to describe the images a documentation corpus references.
type docsImageDescriber struct {
	cli *ChatCLI
}

// errNoVisionModel is returned when no configured provider has a vision model.
var errNoVisionModel = errors.New("no vision-capable model is configured (set CHATCLI_VISION_PROVIDER and CHATCLI_VISION_MODEL, or use a provider with a vision model)")

// DescribeImage implements plugins.DocsImageDescriber. The model is the
// active one when it has vision; otherwise the vision pipeline's own
// choice (CHATCLI_VISION_PROVIDER/MODEL, the describer that worked last,
// then the vision models the account can invoke, current provider first).
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
// model chosen. The active model describes when it sees (probed once when
// the catalog does not know it); otherwise — or when it fails — the vision
// pipeline's candidates are tried in turn, the same ones and in the same
// order as the describe-fallback (describeCandidateSeq).
func (d *docsImageDescriber) describe(ctx context.Context, prompt string, img models.ImageContent, span *pulse.Span) (string, string, error) {
	c := d.cli
	if c.manager == nil {
		return "", "", errNoVisionModel
	}
	images := c.compressImagesForVision([]models.ImageContent{img})
	msg := models.Message{Role: "user", Content: prompt, Images: images}
	try := func(provider, model string) (string, error) {
		client, err := c.manager.GetClient(provider, model)
		if err != nil {
			return "", err
		}
		out, err := client.SendPrompt(ctx, prompt, []models.Message{msg}, describeTokensFor(provider, model))
		if err != nil {
			if isImageInputRejected(err) {
				learnedVision.record(c.stateRoot, provider, model, false, c.logger)
			}
			return "", err
		}
		if out = strings.TrimSpace(out); out == "" {
			return "", errEmptyDescription
		}
		return out, nil
	}
	var lastErr error
	tried, activeTried := 0, false
	if p, m := c.Provider, c.Model; p != "" && m != "" && !envVisionOverride() &&
		c.resolveVisionModeCtx(ctx, p, m, true) == visionNative {
		tried, activeTried = 1, true
		desc, err := try(p, m)
		if err == nil {
			span.With("provider", strings.ToLower(p)).With("model", m)
			return desc, strings.ToLower(p) + "/" + m, nil
		}
		lastErr = err
	}
	for cand := range c.describeCandidateSeq(ctx) {
		if tried >= describeMaxAttempts {
			break
		}
		if activeTried && strings.EqualFold(cand.provider, c.Provider) && cand.model == c.Model {
			continue
		}
		tried++
		desc, err := try(cand.provider, cand.model)
		if err != nil {
			lastErr = err
			continue
		}
		rememberDescriber(cand.provider, cand.model)
		span.With("provider", strings.ToLower(cand.provider)).With("model", cand.model)
		return desc, strings.ToLower(cand.provider) + "/" + cand.model, nil
	}
	if lastErr == nil {
		lastErr = errNoVisionModel
	}
	return "", "", lastErr
}

// errEmptyDescription marks a model that answered with no text.
var errEmptyDescription = errors.New("the vision model returned an empty description")

// envVisionOverride reports whether CHATCLI_VISION_PROVIDER/MODEL pin the
// describing model; the explicit choice wins over the active model.
func envVisionOverride() bool {
	return strings.TrimSpace(os.Getenv("CHATCLI_VISION_PROVIDER")) != "" && strings.TrimSpace(os.Getenv("CHATCLI_VISION_MODEL")) != ""
}
