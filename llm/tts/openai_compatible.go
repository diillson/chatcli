/*
 * ChatCLI - OpenAI-compatible TTS provider.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * One implementation serves every backend that speaks the OpenAI speech API
 * shape — POST {base}/audio/speech with a JSON body {model, input, voice,
 * response_format} returning raw audio bytes:
 *
 *   - self-hosted servers (openedai-speech, Kokoro-FastAPI, etc.) — keyless,
 *   - OpenAI TTS.
 */
package tts

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/diillson/chatcli/utils"
	"go.uber.org/zap"
)

const (
	defaultTTSModel    = "tts-1"
	defaultVoice       = "alloy"
	synthTimeout       = 120 * time.Second
	speechPath         = "/audio/speech"
	maxErrorBodyToShow = 300
)

// OpenAICompatible synthesizes speech against an OpenAI-shaped endpoint.
type OpenAICompatible struct {
	baseURL string
	apiKey  string // optional; omitted when empty (self-hosted)
	model   string
	label   string
	client  *http.Client
}

// NewOpenAICompatible builds the provider. baseURL is required; apiKey may be
// empty for keyless self-hosted servers; model falls back to tts-1.
func NewOpenAICompatible(baseURL, apiKey, model, label string, logger *zap.Logger) (*OpenAICompatible, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return nil, fmt.Errorf("tts: base URL is required")
	}
	if !strings.HasPrefix(baseURL, "http://") && !strings.HasPrefix(baseURL, "https://") {
		return nil, fmt.Errorf("tts: base URL must be http(s): %q", baseURL)
	}
	if strings.TrimSpace(model) == "" {
		model = defaultTTSModel
	}
	if strings.TrimSpace(label) == "" {
		label = "openai-compatible"
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	return &OpenAICompatible{
		baseURL: baseURL,
		apiKey:  strings.TrimSpace(apiKey),
		model:   model,
		label:   label,
		client:  utils.NewHTTPClient(logger, synthTimeout),
	}, nil
}

// Name returns the backend label.
func (o *OpenAICompatible) Name() string { return o.label }

// Synthesize posts the text and returns the audio bytes.
func (o *OpenAICompatible) Synthesize(ctx context.Context, text, voice, format string) (Audio, error) {
	if strings.TrimSpace(text) == "" {
		return Audio{}, fmt.Errorf("tts: empty text")
	}
	if strings.TrimSpace(voice) == "" {
		voice = defaultVoice
	}
	if strings.TrimSpace(format) == "" {
		format = "mp3"
	}
	mime, ext := mimeFor(format)

	body, _ := json.Marshal(map[string]interface{}{
		"model":           o.model,
		"input":           text,
		"voice":           voice,
		"response_format": format,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.baseURL+speechPath, bytes.NewReader(body))
	if err != nil {
		return Audio{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if o.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+o.apiKey)
	}

	resp, err := o.client.Do(req)
	if err != nil {
		return Audio{}, fmt.Errorf("tts: request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyToShow))
		return Audio{}, fmt.Errorf("tts: %s returned %d: %s", o.label, resp.StatusCode, strings.TrimSpace(string(snippet)))
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return Audio{}, fmt.Errorf("tts: read response: %w", err)
	}
	if len(data) == 0 {
		return Audio{}, fmt.Errorf("tts: %s returned no audio", o.label)
	}
	return Audio{Data: data, Mime: mime, Ext: ext}, nil
}
