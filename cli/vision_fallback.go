/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"context"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/llm/catalog"
	llmclient "github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/pkg/pulse"
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
//  4. For a model the catalog does not know (a Bedrock application inference
//     profile ARN whose model could not be looked up, a model from a /models
//     listing): the verdict learned for it, or one probe of the model itself
//     when an image is about to be sent (vision_probe.go).
//  5. Otherwise describe-fallback — the safe default, since sending image
//     blocks to a text-only model is a hard API error. A provider that still
//     rejects an image mid-turn is recovered by vision_rejection.go.
func (cli *ChatCLI) resolveVisionMode() visionMode {
	return cli.resolveVisionModeFor(cli.Provider, cli.Model)
}

// resolveVisionModeFor is resolveVisionMode for a named route: the model a
// turn is routed to, which is not always the session's.
func (cli *ChatCLI) resolveVisionModeFor(provider, model string) visionMode {
	return cli.resolveVisionModeCtx(context.Background(), provider, model, false)
}

// resolveVisionModeCtx is resolveVisionModeFor with one more layer for the
// models the catalog does not know: a learned verdict, or — with
// allowProbe, when an image is actually about to be sent — one probe of the
// model itself (vision_probe.go). Catalog-known models keep exactly the
// previous precedence; only "unknown" changes.
func (cli *ChatCLI) resolveVisionModeCtx(ctx context.Context, provider, model string, allowProbe bool) visionMode {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CHATCLI_VISION_INPUT"))) {
	case "native", "on", "force":
		return visionNative
	case "describe", "caption", "fallback":
		return visionDescribe
	case "off", "none", "ignore":
		return visionOff
	}
	sees, known := catalogKnowsVision(provider, model)
	if known && sees {
		return visionNative
	}
	if modelIDImpliesVision(model) {
		cli.logger.Debug("vision: off-catalog model id implies vision, sending native",
			zap.String("provider", provider), zap.String("model", model))
		return visionNative
	}
	if known {
		return visionDescribe
	}
	if sees, ok := cli.learnedVisionFor(ctx, provider, model, allowProbe); ok {
		if sees {
			return visionNative
		}
		return visionDescribe
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
	ctx, span := beginMediaPattern(ctx, pulseVisionDescribe)
	span.With("images", strconv.Itoa(len(images))).With("bytes_in", strconv.Itoa(imagesBytes(images)))

	candidates := cli.describeCandidates(ctx)
	if len(candidates) == 0 {
		span.Outcome(pulse.StatusError, "no vision model")
		return "", false
	}
	instruction := i18n.T("vision.describe.instruction")
	userMsg := models.Message{Role: "user", Content: instruction, Images: images}

	// Try the candidates in order: a catalog model the account cannot use
	// (not enabled in this Bedrock account or region, no key for that
	// provider) fails fast and the next one is tried, instead of the whole
	// fallback giving up on the first.
	var provider, model, desc string
	var lastErr error
	for i, c := range candidates {
		if i >= describeMaxAttempts {
			break
		}
		client, err := cli.manager.GetClient(c.provider, c.model)
		if err != nil {
			lastErr = err
			continue
		}
		if cli.animation != nil {
			cli.animation.UpdateMessage(i18n.T("vision.describe.in_progress", c.model))
		}
		out, err := client.SendPrompt(ctx, instruction, []models.Message{userMsg}, describeMaxTokens)
		if err != nil {
			lastErr = err
			if isImageInputRejected(err) {
				learnedVision.record(cli.stateRoot, c.provider, c.model, false, cli.logger)
			}
			cli.logger.Debug("vision describe-fallback: candidate failed, trying the next",
				zap.String("provider", c.provider), zap.String("model", c.model), zap.Error(err))
			continue
		}
		if out = strings.TrimSpace(out); out == "" {
			continue
		}
		provider, model, desc = c.provider, c.model, out
		break
	}
	if desc == "" {
		cli.logger.Warn("vision describe-fallback: no candidate could describe the image",
			zap.Int("tried", minInt(len(candidates), describeMaxAttempts)), zap.Error(lastErr))
		span.Outcome(pulse.StatusOf(lastErr), "describe failed")
		return "", false
	}
	rememberDescriber(provider, model)
	span.With("provider", strings.ToLower(provider)).With("model", model)
	span.With("chars_out", strconv.Itoa(len(desc))).Outcome(pulse.StatusOK, "described")

	// Wrap the description so the downstream (text-only) model knows this is a
	// machine-generated transcription of one or more attached images.
	return "\n\n" + i18n.T("vision.describe.note", len(images), model) + "\n" + desc + "\n", true
}

// describeMaxAttempts bounds the candidates one describe-fallback tries.
const describeMaxAttempts = 4

// visionRoute is one provider+model candidate.
type visionRoute struct{ provider, model string }

var lastDescriber struct {
	sync.Mutex
	route visionRoute
}

func rememberDescriber(provider, model string) {
	lastDescriber.Lock()
	lastDescriber.route = visionRoute{provider, model}
	lastDescriber.Unlock()
}

// describeCandidates orders the models that may caption an image: the
// explicit CHATCLI_VISION_PROVIDER/MODEL pair, the describer that worked
// last, then — per provider, the session's first — the vision models the
// account can actually invoke (the provider's own model listing, in the
// invokable id form: a Bedrock inference profile rather than the bare
// foundation id) ahead of the remaining catalog vision models. Models
// already learned to be blind are skipped; the list is de-duplicated.
func (cli *ChatCLI) describeCandidates(ctx context.Context) []visionRoute {
	var out []visionRoute
	seen := map[string]bool{}
	add := func(p, m string) {
		k := visionVerdictKey(p, m)
		if p == "" || m == "" || seen[k] {
			return
		}
		if sees, known := learnedVision.lookup(cli.stateRoot, p, m); known && !sees {
			return
		}
		seen[k] = true
		out = append(out, visionRoute{p, m})
	}
	if p := strings.TrimSpace(os.Getenv("CHATCLI_VISION_PROVIDER")); p != "" {
		add(p, strings.TrimSpace(os.Getenv("CHATCLI_VISION_MODEL")))
	}
	if cli.manager == nil {
		return out
	}
	available := cli.manager.GetAvailableProviders()
	lastDescriber.Lock()
	last := lastDescriber.route
	lastDescriber.Unlock()
	if providerAvailable(available, last.provider) {
		add(last.provider, last.model)
	}
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
		for _, id := range cli.invokableModels(ctx, p) {
			if meta, ok := catalog.Resolve(p, id); ok && metaHasVision(meta) {
				add(p, id)
			}
		}
		for _, meta := range catalog.ListByProvider(p) {
			if metaHasVision(meta) {
				add(p, meta.ID)
			}
		}
	}
	return out
}

// invokableListTTL is how long a provider's model listing is reused; a
// failed listing is retried sooner.
const (
	invokableListTTL       = time.Hour
	invokableListFailedTTL = 5 * time.Minute
	invokableListTimeout   = 10 * time.Second
)

type invokableListing struct {
	ids []string
	at  time.Time
	ok  bool
}

var invokableCache sync.Map // provider (upper) → invokableListing

// invokableModels is the provider's own listing of the models this account
// can call (ListModelsForProvider, API source only), cached per
// provider. Empty when the provider has no live listing or it failed.
func (cli *ChatCLI) invokableModels(ctx context.Context, provider string) []string {
	key := strings.ToUpper(provider)
	if v, ok := invokableCache.Load(key); ok {
		l := v.(invokableListing)
		ttl := invokableListTTL
		if !l.ok {
			ttl = invokableListFailedTTL
		}
		if time.Since(l.at) < ttl {
			return l.ids
		}
	}
	listing := invokableListing{at: time.Now()}
	defer func() { invokableCache.Store(key, listing) }()
	lctx, cancel := context.WithTimeout(ctx, invokableListTimeout)
	defer cancel()
	infos, err := cli.manager.ListModelsForProvider(lctx, provider)
	if err != nil {
		cli.logger.Debug("vision describe-fallback: model listing failed", zap.String("provider", provider), zap.Error(err))
		return nil
	}
	for _, info := range infos {
		// Only a live listing says what the account can call; the catalog
		// fallback adds nothing over the catalog pass that follows.
		if info.Source == llmclient.ModelSourceAPI {
			listing.ids = append(listing.ids, info.ID)
		}
	}
	listing.ok = true
	return listing.ids
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
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
