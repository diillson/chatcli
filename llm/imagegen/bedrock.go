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
	"os"
	"strings"

	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/diillson/chatcli/llm/bedrock"
	"go.uber.org/zap"
)

// defaultBedrockImageModel is a current (non-legacy) Stability model. Nova
// Canvas — the previous default — is legacy with an EOL of 2026-09-30, so new
// users default to Stable Image Core (broadly available, cheapest, fast).
const defaultBedrockImageModel = "stability.stable-image-core-v1:1"

// Bedrock generates images via bedrock-runtime InvokeModel.
type Bedrock struct {
	runtime *bedrockruntime.Client
	model   string
	region  string
}

// NewBedrock builds the provider, loading AWS config from the standard chain.
func NewBedrock(ctx context.Context, region, profile, model string, logger *zap.Logger) (*Bedrock, error) {
	if logger == nil {
		logger = zap.NewNop()
	}
	if strings.TrimSpace(model) == "" {
		model = defaultBedrockImageModel
	}
	rt, resolvedRegion, err := bedrock.LoadBedrockRuntime(ctx, region, profile, logger)
	if err != nil {
		return nil, fmt.Errorf("imagegen: bedrock init: %w", err)
	}
	return &Bedrock{runtime: rt, model: model, region: resolvedRegion}, nil
}

// Name returns "bedrock".
func (*Bedrock) Name() string { return "bedrock" }

// Generate invokes the configured Bedrock image model (Stability text-to-image
// or the legacy Amazon TEXT_IMAGE task, selected per model family).
func (b *Bedrock) Generate(ctx context.Context, prompt string, opts Options) ([]Image, error) {
	if strings.TrimSpace(prompt) == "" {
		return nil, fmt.Errorf("imagegen: empty prompt")
	}
	body := buildBedrockRequest(b.model, prompt, opts)

	out, err := b.runtime.InvokeModel(ctx, &bedrockruntime.InvokeModelInput{
		ModelId:     strPtr(b.model),
		ContentType: strPtr("application/json"),
		Accept:      strPtr("application/json"),
		Body:        body,
	})
	if err != nil {
		return nil, fmt.Errorf("imagegen: bedrock InvokeModel: %w", err)
	}
	return parseBedrockImages(out.Body)
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
		body, _ := json.Marshal(map[string]interface{}{
			"prompt":        prompt,
			"mode":          "text-to-image",
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
	for _, k := range []string{"BEDROCK_REGION", "AWS_REGION", "AWS_DEFAULT_REGION"} {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return ""
}

func bedrockImageProfile() string {
	for _, k := range []string{"BEDROCK_PROFILE", "AWS_PROFILE"} {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return ""
}
