/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
/*
 * vision_probe.go — learning whether a model the catalog does not know can
 * see.
 *
 * The catalog decides vision for the models it knows. The rest — an
 * application inference profile ARN whose foundation model could not be
 * looked up (no bedrock:GetInferenceProfile permission), a model discovered
 * from a provider's /models API, a brand-new id — used to be treated as
 * blind, so a model that could see an image (a Claude behind a Bedrock
 * profile) had its screenshots described by another model or dropped.
 *
 * For those models the first image is preceded by one minimal probe: a
 * 16×16 PNG and a few-token reply, sent to the model itself. Accepted → the
 * model sees; the provider rejecting the image → it does not; anything else
 * (network, throttling, credentials) proves nothing and the old behavior
 * applies to that call. The verdict is remembered per provider and model,
 * in memory and in a small file under the state directory, for 30 days.
 */
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/diillson/chatcli/llm/catalog"
	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/utils"
	"go.uber.org/zap"
)

const (
	// visionVerdictTTL is how long a learned verdict is trusted; providers
	// add vision to existing models (and profiles get re-pointed).
	visionVerdictTTL = 30 * 24 * time.Hour
	// visionProbeTimeout bounds the probe round-trip.
	visionProbeTimeout = 30 * time.Second
	// visionVerdictFile holds the learned verdicts under the state root.
	visionVerdictFile = "vision_capabilities.json"
)

// probePNG is a small, valid, all-white PNG built with the standard
// encoder (a malformed image would draw a different error and leave the
// probe inconclusive). 16×16 stays above every provider's minimum size.
var probePNG = func() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 16, 16))
	for i := range img.Pix {
		img.Pix[i] = 0xff
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}()

// visionVerdict is one learned answer.
type visionVerdict struct {
	Sees    bool      `json:"sees"`
	Learned time.Time `json:"learned"`
}

// visionVerdicts caches learned verdicts for this process and persists
// them under the state root.
type visionVerdicts struct {
	mu     sync.Mutex
	loaded bool
	path   string
	m      map[string]visionVerdict
}

var learnedVision = &visionVerdicts{}

func visionVerdictKey(provider, model string) string {
	return strings.ToUpper(strings.TrimSpace(provider)) + "|" + strings.TrimSpace(model)
}

// lookup returns a fresh learned verdict.
func (v *visionVerdicts) lookup(root, provider, model string) (sees, known bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.loadLocked(root)
	got, ok := v.m[visionVerdictKey(provider, model)]
	if !ok || time.Since(got.Learned) > visionVerdictTTL {
		return false, false
	}
	return got.Sees, true
}

// record stores a verdict and persists the set (best effort).
func (v *visionVerdicts) record(root, provider, model string, sees bool, logger *zap.Logger) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.loadLocked(root)
	v.m[visionVerdictKey(provider, model)] = visionVerdict{Sees: sees, Learned: time.Now()}
	if v.path == "" {
		return
	}
	data, err := json.MarshalIndent(v.m, "", "  ")
	if err == nil {
		err = utils.AtomicWriteFile(v.path, data, 0o600)
	}
	if err != nil && logger != nil {
		logger.Debug("vision: could not persist learned capabilities", zap.Error(err))
	}
}

func (v *visionVerdicts) loadLocked(root string) {
	path := ""
	if strings.TrimSpace(root) != "" {
		path = filepath.Join(root, visionVerdictFile)
	}
	if v.loaded && v.path == path {
		return
	}
	v.loaded, v.path, v.m = true, path, map[string]visionVerdict{}
	if path == "" {
		return
	}
	data, err := os.ReadFile(path) // #nosec G304 -- fixed file name under the state root
	if err != nil {
		return
	}
	var onDisk map[string]visionVerdict
	if json.Unmarshal(data, &onDisk) == nil {
		for k, val := range onDisk {
			v.m[k] = val
		}
	}
}

// imageRejectionMarkers are the provider error phrasings for "this model
// does not take image input" (Bedrock, OpenAI-compatible, Anthropic,
// Gemini, Ollama, DeepSeek, Mistral). Matched lower-cased.
var imageRejectionMarkers = []string{
	"does not support image", "doesn't support image", "do not support image",
	"does not support the image", "doesn't support the image",
	"image input is not supported", "images are not supported", "image is not supported",
	"image_url is only supported", "unknown variant `image_url`", "unknown variant \"image_url\"",
	"does not support multimodal", "multimodal input is not supported", "vision is not supported",
	"model does not support vision", "not a multimodal model", "image content block",
	"invalid content type. image_url",
}

// isImageInputRejected reports whether err is the provider refusing image
// input (as opposed to a transport, quota or credential failure).
func isImageInputRejected(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, m := range imageRejectionMarkers {
		if strings.Contains(msg, m) {
			return true
		}
	}
	return false
}

// errVisionProbeInconclusive marks a probe that proved nothing.
var errVisionProbeInconclusive = errors.New("vision probe inconclusive")

// probeVision asks the model itself whether it takes image input.
func (cli *ChatCLI) probeVision(ctx context.Context, provider, model string) (bool, error) {
	if cli.manager == nil {
		return false, errVisionProbeInconclusive
	}
	client, err := cli.manager.GetClient(provider, model)
	if err != nil || client == nil {
		return false, errVisionProbeInconclusive
	}
	ctx, cancel := context.WithTimeout(ctx, visionProbeTimeout)
	defer cancel()
	msg := models.Message{
		Role:    "user",
		Content: "Reply with the single word ok.",
		Images:  []models.ImageContent{{MediaType: "image/png", Data: probePNG, FileName: "probe.png"}},
	}
	_, err = client.SendPrompt(ctx, msg.Content, []models.Message{msg}, 5)
	switch {
	case err == nil:
		return true, nil
	case isImageInputRejected(err):
		return false, nil
	default:
		cli.logger.Debug("vision probe inconclusive", zap.String("provider", provider),
			zap.String("model", model), zap.Error(err))
		return false, errVisionProbeInconclusive
	}
}

// learnedVisionFor resolves vision for a model the catalog does not know:
// a remembered verdict, else one probe. ok=false when nothing could be
// learned (the caller keeps the conservative path).
func (cli *ChatCLI) learnedVisionFor(ctx context.Context, provider, model string, allowProbe bool) (sees, ok bool) {
	if sees, known := learnedVision.lookup(cli.stateRoot, provider, model); known {
		return sees, true
	}
	if !allowProbe {
		return false, false
	}
	sees, err := cli.probeVision(ctx, provider, model)
	if err != nil {
		return false, false
	}
	learnedVision.record(cli.stateRoot, provider, model, sees, cli.logger)
	cli.logger.Info("vision: learned capability of an off-catalog model",
		zap.String("provider", provider), zap.String("model", model), zap.Bool("sees", sees))
	return sees, true
}

// catalogKnowsVision reports whether the catalog has an entry for the
// route, and what it says about vision.
func catalogKnowsVision(provider, model string) (sees, known bool) {
	meta, ok := catalog.Resolve(provider, model)
	if !ok {
		return false, false
	}
	return metaHasVision(meta), true
}
