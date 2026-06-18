/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"context"
	"os"
	"strings"

	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/llm/catalog"
	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

// describeMaxTokens caps the captioning response. A few paragraphs of
// description is plenty; we do not want the bridge to balloon the prompt.
const describeMaxTokens = 1500

// visionMode is how attached images are handled for the active model.
type visionMode int

const (
	visionNative   visionMode = iota // send native image blocks to the model
	visionDescribe                   // caption via a vision model, fold into prompt
	visionOff                        // ignore attached images entirely
)

// resolveVisionMode decides how to treat attached images for the active
// provider+model. Layered, authoritative-first, so an off-catalog model
// (one fetched from the provider's /models API with no catalog entry) is still
// handled correctly:
//
//  1. CHATCLI_VISION_INPUT explicit override — native | describe | off. This is
//     the escape hatch when you KNOW an off-catalog model does or doesn't see.
//  2. The catalog vision capability (authoritative for known models).
//  3. A conservative heuristic for off-catalog ids that are *unambiguously*
//     vision models (the id literally carries a vision marker like "-vl" or
//     "vision"); these names exist only for multimodal models, so the
//     false-positive risk is near zero.
//  4. Otherwise describe-fallback — the safe default, since sending image
//     blocks to a text-only model is a hard API error.
func (cli *ChatCLI) resolveVisionMode() visionMode {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CHATCLI_VISION_INPUT"))) {
	case "native", "on", "force":
		return visionNative
	case "describe", "caption", "fallback":
		return visionDescribe
	case "off", "none", "ignore":
		return visionOff
	}
	if catalog.HasCapability(cli.Provider, cli.Model, "vision") {
		return visionNative
	}
	if modelIDImpliesVision(cli.Model) {
		cli.logger.Debug("vision: off-catalog model id implies vision, sending native",
			zap.String("provider", cli.Provider), zap.String("model", cli.Model))
		return visionNative
	}
	return visionDescribe
}

// visionIDMarkers are substrings that appear only in multimodal model ids, so a
// match is a near-certain vision model even when the catalog has no entry.
var visionIDMarkers = []string{
	"vision", "-vl", "vl-", "-vl-", "_vl", "vl_",
	"pixtral", "llava", "internvl", "qwen-vl", "qwen2-vl", "qwen2.5-vl",
	"multimodal", "omni",
}

// modelIDImpliesVision reports whether a model id unambiguously names a vision
// model. Deliberately conservative: it matches explicit vision markers only,
// never broad family prefixes (which have text-only exceptions such as
// claude-3-5-haiku or o3-mini), because a false positive sends an image block
// to a text-only model and hard-errors the request.
func modelIDImpliesVision(model string) bool {
	m := strings.ToLower(strings.TrimSpace(model))
	for _, marker := range visionIDMarkers {
		if strings.Contains(m, marker) {
			return true
		}
	}
	return false
}

// describeImagesFallback implements the "A" half of the hybrid vision
// strategy: when the active model has no native vision, it routes the
// attached images through a separate vision-capable model that returns a
// textual description, which the caller folds into the prompt so a
// text-only provider can still reason about the image's content.
//
// It returns the description text and ok=true on success, or ok=false when
// no vision-capable model is configured/reachable (the caller then warns and
// drops the image rather than sending something a model cannot read).
func (cli *ChatCLI) describeImagesFallback(ctx context.Context, images []models.ImageContent) (string, bool) {
	if len(images) == 0 {
		return "", false
	}

	provider, model, ok := cli.resolveVisionDescribeModel()
	if !ok {
		return "", false
	}

	client, err := cli.manager.GetClient(provider, model)
	if err != nil {
		cli.logger.Warn("vision describe-fallback: falha ao obter client",
			zap.String("provider", provider), zap.String("model", model), zap.Error(err))
		return "", false
	}

	instruction := i18n.T("vision.describe.instruction")
	userMsg := models.Message{Role: "user", Content: instruction, Images: images}

	cli.animation.UpdateMessage(i18n.T("vision.describe.in_progress", model))
	desc, err := client.SendPrompt(ctx, instruction, []models.Message{userMsg}, describeMaxTokens)
	if err != nil {
		cli.logger.Warn("vision describe-fallback: SendPrompt falhou",
			zap.String("provider", provider), zap.String("model", model), zap.Error(err))
		return "", false
	}
	desc = strings.TrimSpace(desc)
	if desc == "" {
		return "", false
	}

	// Wrap the description so the downstream (text-only) model knows this is a
	// machine-generated transcription of one or more attached images.
	return "\n\n" + i18n.T("vision.describe.note", len(images), model) + "\n" + desc + "\n", true
}

// resolveVisionDescribeModel picks the provider+model used to caption images
// for the describe-fallback. Resolution order:
//  1. explicit CHATCLI_VISION_PROVIDER + CHATCLI_VISION_MODEL env override;
//  2. a vision-capable model on the CURRENT provider (cheapest auth-wise);
//  3. the first vision-capable model on any other available provider.
//
// Returns ok=false when no available provider exposes a vision model.
func (cli *ChatCLI) resolveVisionDescribeModel() (string, string, bool) {
	if p := strings.TrimSpace(os.Getenv("CHATCLI_VISION_PROVIDER")); p != "" {
		if m := strings.TrimSpace(os.Getenv("CHATCLI_VISION_MODEL")); m != "" {
			return p, m, true
		}
	}

	available := cli.manager.GetAvailableProviders()

	// Prefer the current provider so we reuse already-configured credentials.
	ordered := make([]string, 0, len(available)+1)
	if cli.Provider != "" {
		ordered = append(ordered, cli.Provider)
	}
	for _, p := range available {
		if !strings.EqualFold(p, cli.Provider) {
			ordered = append(ordered, p)
		}
	}

	for _, p := range ordered {
		if !providerAvailable(available, p) {
			continue
		}
		for _, meta := range catalog.ListByProvider(p) {
			if metaHasVision(meta) {
				return p, meta.ID, true
			}
		}
	}
	return "", "", false
}

func providerAvailable(available []string, p string) bool {
	for _, a := range available {
		if strings.EqualFold(a, p) {
			return true
		}
	}
	return false
}

func metaHasVision(meta catalog.ModelMeta) bool {
	for _, c := range meta.Capabilities {
		if c == "vision" {
			return true
		}
	}
	return false
}
