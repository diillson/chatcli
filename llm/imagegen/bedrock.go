/*
 * ChatCLI - AWS Bedrock image generation provider.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Bedrock generates images through its dedicated image models via bedrock-runtime
 * InvokeModel (AWS SigV4), NOT through the OpenAI shape and NOT through the
 * chat/gpt models. Two request families are covered: the current Stability AI
 * models (Stable Image Core/Ultra, SD3.5 Large) use a text-to-image shape, while
 * the legacy Amazon models (Nova Canvas, Titan Image Generator) share the
 * TEXT_IMAGE shape. Both return {"images":["<b64>"]}. Credentials reuse the same
 * chain as the chat provider (llm/bedrock.LoadBedrockRuntime: env / shared
 * profile / IAM role).
 */
package imagegen

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/diillson/chatcli/llm/bedrock"
	"go.uber.org/zap"
)

// defaultBedrockImageModel is the cheapest current Stability model. Nova
// Canvas, the previous default, reached its end of life on 2026-09-30.
const defaultBedrockImageModel = "stability.stable-image-core-v1:1"

// Bedrock generates images via bedrock-runtime InvokeModel.
type Bedrock struct {
	runtime *bedrockruntime.Client
	model   string // as configured
	invoke  string // the id sent to InvokeModel (a "us." profile when the model requires one)
	region  string
}

// NewBedrock builds the provider, loading AWS config from the standard chain.
// Bedrock offers its image models in few regions (Core/Ultra/SD3.5 only in
// us-west-2), so the client runs in a region that offers the model: the
// configured one when it does, the model's own otherwise.
func NewBedrock(ctx context.Context, region, profile, model string, logger *zap.Logger) (*Bedrock, error) {
	if logger == nil {
		logger = zap.NewNop()
	}
	if strings.TrimSpace(model) == "" {
		model = defaultBedrockImageModel
	}
	useRegion := bedrockRegionFor(model, region)
	if useRegion != region && region != "" {
		logger.Info("imagegen: Bedrock image model is not offered in the configured region; using its region",
			zap.String("model", model), zap.String("configured", region), zap.String("region", useRegion))
	}
	rt, resolvedRegion, err := bedrock.LoadBedrockRuntime(ctx, useRegion, profile, logger)
	if err != nil {
		return nil, fmt.Errorf("imagegen: bedrock init: %w", err)
	}
	return &Bedrock{runtime: rt, model: model, invoke: bedrockInvokeID(model, resolvedRegion), region: resolvedRegion}, nil
}

// bedrockRegionFor picks the region to invoke model in: configured when the
// catalog says the model is offered there (or the model is unknown), else
// the model's first region.
func bedrockRegionFor(model, configured string) string {
	m, _, ok := LookupModel(model)
	regions := m.RegionList()
	if !ok || len(regions) == 0 || containsString(regions, configured) {
		return configured
	}
	return regions[0]
}

// bedrockInvokeID returns the id to invoke: models offered only through the
// US cross-region profile get the "us." prefix (Bedrock rejects their bare
// id with "on-demand throughput isn't supported").
func bedrockInvokeID(model, region string) string {
	if strings.HasPrefix(model, "us.") {
		return model
	}
	if m, _, ok := LookupModel(model); ok && m.Profile && strings.HasPrefix(region, "us-") {
		return "us." + model
	}
	return model
}

// Name returns "bedrock".
func (*Bedrock) Name() string { return "bedrock" }

// Model returns the Bedrock image model id.
func (b *Bedrock) Model() string { return b.model }

// Region returns the AWS region the model is invoked in.
func (b *Bedrock) Region() string { return b.region }

// Bedrock request families. Editing and generation are per-model on Bedrock:
// Core only generates, SD3.5 Large and Ultra do both, and the Stability
// Image Services take an input image (each with its own fields).
const (
	bedrockEditNone          = ""
	bedrockEditSD35          = "sd35"         // mode "image-to-image" + image + strength
	bedrockEditUltra         = "ultra"        // image + strength, no mode
	bedrockEditAmazon        = "amazon"       // IMAGE_VARIATION (Nova Canvas / Titan)
	bedrockEditInpaint       = "inpaint"      // image + prompt + optional mask
	bedrockEditErase         = "erase"        // image + optional mask
	bedrockEditRemoveBG      = "remove-bg"    // image
	bedrockEditControl       = "control"      // image + prompt (sketch / structure)
	bedrockEditStyleGuide    = "style-guide"  // image + prompt
	bedrockEditUpscalePrompt = "upscale"      // image + prompt (creative / conservative)
	bedrockEditFastUpscale   = "fast-upscale" // image
)

// bedrockEditFamily maps a model id (or a cross-region inference profile such
// as "us.stability.stable-image-inpaint-v1:0") to its edit request family.
func bedrockEditFamily(model string) string {
	m := strings.ToLower(strings.TrimSpace(model))
	switch {
	case strings.Contains(m, "stability.sd3-5-large"):
		return bedrockEditSD35
	case strings.Contains(m, "stability.stable-image-ultra"):
		return bedrockEditUltra
	case strings.Contains(m, "stability.stable-image-inpaint"):
		return bedrockEditInpaint
	case strings.Contains(m, "stability.stable-image-erase-object"):
		return bedrockEditErase
	case strings.Contains(m, "stability.stable-image-remove-background"):
		return bedrockEditRemoveBG
	case strings.Contains(m, "stability.stable-image-control-sketch"), strings.Contains(m, "stability.stable-image-control-structure"):
		return bedrockEditControl
	case strings.Contains(m, "stability.stable-image-style-guide"):
		return bedrockEditStyleGuide
	case strings.Contains(m, "stability.stable-creative-upscale"), strings.Contains(m, "stability.stable-conservative-upscale"):
		return bedrockEditUpscalePrompt
	case strings.Contains(m, "stability.stable-fast-upscale"):
		return bedrockEditFastUpscale
	case strings.Contains(m, "amazon.nova-canvas"), strings.Contains(m, "amazon.titan-image"):
		return bedrockEditAmazon
	}
	return bedrockEditNone
}

// bedrockEditOnly reports whether model needs an input image (the Image
// Services): it cannot generate from text alone.
func bedrockEditOnly(model string) bool {
	switch bedrockEditFamily(model) {
	case bedrockEditNone, bedrockEditSD35, bedrockEditUltra, bedrockEditAmazon:
		return false
	}
	return true
}

// bedrockNeedsPrompt reports whether the edit family uses the prompt.
func bedrockNeedsPrompt(family string) bool {
	switch family {
	case bedrockEditErase, bedrockEditRemoveBG, bedrockEditFastUpscale:
		return false
	}
	return true
}

// supportsEdit reports whether the configured model edits images.
func (b *Bedrock) supportsEdit() bool { return bedrockEditFamily(b.model) != bedrockEditNone }

// Generate invokes the configured Bedrock image model (Stability text-to-image
// or the legacy Amazon TEXT_IMAGE task, selected per model family).
func (b *Bedrock) Generate(ctx context.Context, prompt string, opts Options) ([]Image, error) {
	if strings.TrimSpace(prompt) == "" {
		return nil, fmt.Errorf("imagegen: empty prompt")
	}
	if bedrockEditOnly(b.model) {
		return nil, fmt.Errorf("imagegen: bedrock model %s only transforms an input image; use @image edit, or a generation model such as %s", b.model, defaultBedrockImageModel)
	}
	return b.invokeModel(ctx, buildBedrockRequest(b.model, prompt, opts))
}

// Edit transforms an input image, in the request shape of the configured
// model's family (see bedrockEditFamily). All of them return the shared
// {"images":["<b64>"]} shape.
func (b *Bedrock) Edit(ctx context.Context, prompt string, inputs []Image, opts EditOptions) ([]Image, error) {
	family := bedrockEditFamily(b.model)
	if family == bedrockEditNone {
		return nil, fmt.Errorf("%w (bedrock model %s)", ErrEditUnsupported, b.model)
	}
	if bedrockNeedsPrompt(family) && strings.TrimSpace(prompt) == "" {
		return nil, fmt.Errorf("imagegen: empty prompt")
	}
	if len(inputs) == 0 || len(inputs[0].Data) == 0 {
		return nil, fmt.Errorf("imagegen: edit requires an input image")
	}
	return b.invokeModel(ctx, buildBedrockEditRequest(b.model, prompt, inputs[0].Data, opts))
}

// invokeModel runs InvokeModel and explains the region and profile errors
// Bedrock reports only as "invalid model identifier" or "on-demand
// throughput isn't supported".
func (b *Bedrock) invokeModel(ctx context.Context, body []byte) ([]Image, error) {
	out, err := b.runtime.InvokeModel(ctx, &bedrockruntime.InvokeModelInput{
		ModelId:     strPtr(b.invoke),
		ContentType: strPtr("application/json"),
		Accept:      strPtr("application/json"),
		Body:        body,
	})
	if err != nil {
		return nil, explainBedrockInvokeError(b.model, b.region, err)
	}
	return parseBedrockImages(out.Body)
}

// explainBedrockInvokeError adds what to do to Bedrock's terse errors.
func explainBedrockInvokeError(model, region string, err error) error {
	msg := err.Error()
	hint := ""
	switch {
	case strings.Contains(msg, "model identifier is invalid"):
		if m, _, ok := LookupModel(model); ok && m.Regions != "" {
			hint = fmt.Sprintf("%s is offered in %s, not in %s", model, strings.ReplaceAll(m.Regions, ",", ", "), region)
		} else {
			hint = fmt.Sprintf("%s is not offered in %s; @image models lists what Bedrock offers and where", model, region)
		}
	case strings.Contains(msg, "on-demand throughput"):
		hint = fmt.Sprintf("%s runs only through a cross-region inference profile: use us.%s from us-east-1, us-east-2 or us-west-2", model, strings.TrimPrefix(model, "us."))
	case strings.Contains(msg, "AccessDeniedException") || strings.Contains(msg, "don't have access"):
		hint = "enable access to this model in the Bedrock console (Model access) for region " + region
	}
	if hint != "" {
		return fmt.Errorf("imagegen: bedrock: %s: %w", hint, err)
	}
	return fmt.Errorf("imagegen: bedrock InvokeModel: %w", err)
}

// buildBedrockEditRequest builds the InvokeModel body for the model's edit
// family. SD3.5 Large: mode "image-to-image" with the base64 image +
// strength. Stable Image Ultra: image + strength without mode. Image
// Services: the image plus the fields each one takes. Amazon (Nova Canvas /
// Titan): IMAGE_VARIATION conditioned on the input image.
func buildBedrockEditRequest(model, prompt string, img []byte, opts EditOptions) []byte {
	b64 := base64.StdEncoding.EncodeToString(img)
	req := map[string]interface{}{"image": b64, "output_format": "png"}
	switch family := bedrockEditFamily(model); family {
	case bedrockEditSD35, bedrockEditUltra:
		strength := opts.Strength
		if strength <= 0 || strength > 1 {
			strength = 0.6
		}
		req["prompt"] = prompt
		req["strength"] = strength
		if family == bedrockEditSD35 {
			req["mode"] = "image-to-image"
		}
	case bedrockEditInpaint:
		req["prompt"] = prompt
		if len(opts.Mask) > 0 {
			req["mask"] = base64.StdEncoding.EncodeToString(opts.Mask)
		}
	case bedrockEditErase:
		if len(opts.Mask) > 0 {
			req["mask"] = base64.StdEncoding.EncodeToString(opts.Mask)
		}
	case bedrockEditControl:
		req["prompt"] = prompt
		if opts.Strength > 0 && opts.Strength <= 1 {
			req["control_strength"] = opts.Strength
		}
	case bedrockEditStyleGuide:
		req["prompt"] = prompt
		if opts.Strength > 0 && opts.Strength <= 1 {
			req["fidelity"] = opts.Strength
		}
	case bedrockEditUpscalePrompt:
		req["prompt"] = prompt
		if strings.Contains(strings.ToLower(model), "creative") && opts.Strength > 0 && opts.Strength <= 0.35 {
			req["creativity"] = opts.Strength
		}
	case bedrockEditRemoveBG, bedrockEditFastUpscale:
		// image only
	default:
		return buildBedrockVariationRequest(prompt, b64, opts)
	}
	body, _ := json.Marshal(req)
	return body
}

// buildBedrockVariationRequest is the Amazon IMAGE_VARIATION body.
func buildBedrockVariationRequest(prompt, b64 string, opts EditOptions) []byte {
	// Amazon Nova Canvas / Titan IMAGE_VARIATION. similarityStrength (0.2–1.0):
	// higher keeps the result closer to the source; 0.7 preserves composition
	// while letting the prompt steer the change.
	sim := opts.Strength
	if sim < 0.2 || sim > 1.0 {
		sim = 0.7
	}
	w, h := parseSize(opts.Size)
	body, _ := json.Marshal(map[string]interface{}{
		"taskType": "IMAGE_VARIATION",
		"imageVariationParams": map[string]interface{}{
			"text":               prompt,
			"images":             []string{b64},
			"similarityStrength": sim,
		},
		"imageGenerationConfig": map[string]interface{}{
			"numberOfImages": 1,
			"width":          w,
			"height":         h,
		},
	})
	return body
}

// buildBedrockRequest builds the InvokeModel body per model family. Nova Canvas
// and Titan share the TEXT_IMAGE shape; Stability models use a text-to-image
// shape with an aspect ratio. The {"images":["<b64>"]} response is shared.
func buildBedrockRequest(model, prompt string, opts Options) []byte {
	w, h := parseSize(opts.Size)
	n := opts.N
	if n <= 0 {
		n = 1
	}
	if strings.HasPrefix(strings.ToLower(model), "stability.") {
		// No "mode": text-to-image is every Stability model's default, and
		// only SD3.5 Large documents the field.
		body, _ := json.Marshal(map[string]interface{}{
			"prompt":        prompt,
			"aspect_ratio":  aspectRatio(w, h),
			"output_format": "png",
		})
		return body
	}
	body, _ := json.Marshal(map[string]interface{}{
		"taskType":          "TEXT_IMAGE",
		"textToImageParams": map[string]interface{}{"text": prompt},
		"imageGenerationConfig": map[string]interface{}{
			"numberOfImages": n,
			"width":          w,
			"height":         h,
		},
	})
	return body
}

// parseBedrockImages decodes the {"images":["<b64>"]} response shared by the
// Stability text-to-image models and the legacy Amazon Nova Canvas / Titan ones.
func parseBedrockImages(raw []byte) ([]Image, error) {
	var out struct {
		Images []string `json:"images"`
		Error  string   `json:"error"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("imagegen: bedrock decode: %w", err)
	}
	if strings.TrimSpace(out.Error) != "" {
		return nil, fmt.Errorf("imagegen: bedrock: %s", out.Error)
	}
	images := make([]Image, 0, len(out.Images))
	for _, b64 := range out.Images {
		data, err := base64.StdEncoding.DecodeString(b64)
		if err != nil || len(data) == 0 {
			continue
		}
		images = append(images, Image{Data: data, Mime: "image/png", Ext: "png"})
	}
	if len(images) == 0 {
		return nil, fmt.Errorf("imagegen: bedrock returned no decodable images")
	}
	return images, nil
}

func strPtr(s string) *string { return &s }

// aspectRatio maps a width/height to the closest Stability-supported ratio
// string. Stability takes an aspect_ratio, not explicit pixels.
func aspectRatio(w, h int) string {
	if w <= 0 || h <= 0 || w == h {
		return "1:1"
	}
	r := float64(w) / float64(h)
	switch {
	case r >= 1.7:
		return "16:9"
	case r >= 1.4:
		return "3:2"
	case r >= 1.2:
		return "5:4"
	case r <= 0.58:
		return "9:16"
	case r <= 0.7:
		return "2:3"
	case r <= 0.85:
		return "4:5"
	default:
		return "1:1"
	}
}

// bedrockImageRegion / bedrockImageProfile read the same env the chat Bedrock
// provider uses.
func bedrockImageRegion() string {
	region, _ := bedrock.ResolveRegion()
	return region
}

func bedrockImageProfile() string {
	profile, _ := bedrock.ResolveProfile()
	return profile
}
