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
