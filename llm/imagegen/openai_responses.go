/*
 * ChatCLI - OpenAI Responses-API image generation provider.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Unlike the Images API (/images/generations, used by the dedicated image
 * models like gpt-image-1), the Responses API lets a general chat/reasoning
 * model (e.g. gpt-5.5) generate images via the built-in image_generation tool:
 *
 *   POST {base}/responses
 *   { "model": "gpt-5.5", "input": "<prompt>", "tools": [{"type":"image_generation"}] }
 *
 * The response's output array contains an "image_generation_call" item whose
 * "result" is a base64 PNG. This backend covers that path so users can pick
 * either API.
 */
package imagegen

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/diillson/chatcli/utils"
	"go.uber.org/zap"
)

const responsesPath = "/responses"

// OpenAIResponses generates images through the Responses API image_generation
// tool, driven by a chat/reasoning model.
type OpenAIResponses struct {
	baseURL string
	apiKey  string
	model   string
	client  *http.Client
}

// NewOpenAIResponses builds the provider. apiKey is required; model defaults to
// a current general model when empty.
func NewOpenAIResponses(baseURL, apiKey, model string, logger *zap.Logger) (*OpenAIResponses, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		baseURL = openAIBaseURL
	}
	if !strings.HasPrefix(baseURL, "http://") && !strings.HasPrefix(baseURL, "https://") {
		return nil, fmt.Errorf("imagegen: base URL must be http(s): %q", baseURL)
	}
	if strings.TrimSpace(apiKey) == "" {
		return nil, fmt.Errorf("imagegen: API key is required for the Responses API")
	}
	if strings.TrimSpace(model) == "" {
		model = "gpt-5.5"
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	return &OpenAIResponses{
		baseURL: baseURL,
		apiKey:  strings.TrimSpace(apiKey),
		model:   model,
		client:  utils.NewHTTPClient(logger, imageGenTimeout),
	}, nil
}

// Name returns "openai-responses".
func (*OpenAIResponses) Name() string { return "openai-responses" }

// Generate asks the model to produce an image via the image_generation tool.
func (o *OpenAIResponses) Generate(ctx context.Context, prompt string, opts Options) ([]Image, error) {
	if strings.TrimSpace(prompt) == "" {
		return nil, fmt.Errorf("imagegen: empty prompt")
	}
	tool := map[string]interface{}{"type": "image_generation"}
	if strings.TrimSpace(opts.Size) != "" {
		tool["size"] = opts.Size
	}
	body, _ := json.Marshal(map[string]interface{}{
		"model": o.model,
		"input": prompt,
		"tools": []map[string]interface{}{tool},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.baseURL+responsesPath, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+o.apiKey)

	resp, err := o.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("imagegen: request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrBody))
		return nil, fmt.Errorf("imagegen: responses API returned %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
	}

	// The output array carries items of various types; we want the
	// image_generation_call result (base64 PNG).
	var out struct {
		Output []struct {
			Type   string `json:"type"`
			Result string `json:"result"`
		} `json:"output"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("imagegen: decode response: %w", err)
	}
	images := make([]Image, 0, len(out.Output))
	for _, item := range out.Output {
		if !strings.Contains(item.Type, "image_generation") || item.Result == "" {
			continue
		}
		raw, derr := base64.StdEncoding.DecodeString(item.Result)
		if derr != nil || len(raw) == 0 {
			continue
		}
		images = append(images, Image{Data: raw, Mime: "image/png", Ext: "png"})
	}
	if len(images) == 0 {
		return nil, fmt.Errorf("imagegen: responses API returned no image (model %q may not support image_generation)", o.model)
	}
	return images, nil
}
