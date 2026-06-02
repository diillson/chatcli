/*
 * ChatCLI - OpenAI-compatible transcription provider.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * One implementation serves every backend that speaks the OpenAI audio API
 * shape — POST {base}/audio/transcriptions as multipart/form-data with a
 * `file` part and a `model` field:
 *
 *   - self-hosted whisper.cpp / faster-whisper / Speaches (keyless),
 *   - OpenAI Whisper,
 *   - Groq Whisper.
 *
 * The only differences are the base URL, the API key (optional for
 * self-hosted) and the model, so they collapse into this one type.
 */
package transcription

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	"github.com/diillson/chatcli/utils"
	"go.uber.org/zap"
)

const (
	defaultModel       = "whisper-1"
	transcribeTimeout  = 120 * time.Second // upload + server-side decode of a voice clip
	transcribePath     = "/audio/transcriptions"
	maxErrorBodyToShow = 300
)

// OpenAICompatible transcribes audio against an OpenAI-shaped endpoint.
type OpenAICompatible struct {
	baseURL string // normalized, no trailing slash, e.g. https://api.openai.com/v1
	apiKey  string // optional; omitted from the request when empty (self-hosted)
	model   string
	label   string // backend label for Name(): "selfhosted" | "openai" | "groq"
	client  *http.Client
}

// NewOpenAICompatible builds the provider. baseURL is required; apiKey may be
// empty for keyless self-hosted servers; model falls back to whisper-1.
func NewOpenAICompatible(baseURL, apiKey, model, label string, logger *zap.Logger) (*OpenAICompatible, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return nil, fmt.Errorf("transcription: base URL is required")
	}
	if !strings.HasPrefix(baseURL, "http://") && !strings.HasPrefix(baseURL, "https://") {
		return nil, fmt.Errorf("transcription: base URL must be http(s): %q", baseURL)
	}
	if strings.TrimSpace(model) == "" {
		model = defaultModel
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
		client:  utils.NewHTTPClient(logger, transcribeTimeout),
	}, nil
}

// Name reports the backend and model, e.g. "selfhosted:whisper-1".
func (o *OpenAICompatible) Name() string { return o.label + ":" + o.model }

// Transcribe uploads the audio and returns the recognized text.
func (o *OpenAICompatible) Transcribe(ctx context.Context, audio []byte, mimeType, filename, language string) (string, error) {
	if len(audio) == 0 {
		return "", fmt.Errorf("transcription: empty audio")
	}
	req, err := o.buildRequest(ctx, audio, mimeType, filename, language)
	if err != nil {
		return "", err
	}
	resp, err := o.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("transcription: request to %s failed: %w", o.baseURL, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("transcription: reading response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("transcription: %s returned %d: %s", o.baseURL, resp.StatusCode, snippet(body))
	}
	return parseTranscript(resp.Header.Get("Content-Type"), body)
}

// buildRequest assembles the multipart POST. It is separated from Transcribe so
// the wire format is unit-testable without a network round-trip.
func (o *OpenAICompatible) buildRequest(ctx context.Context, audio []byte, mimeType, filename, language string) (*http.Request, error) {
	if strings.TrimSpace(filename) == "" {
		filename = "audio" + extensionForMime(mimeType)
	}

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	part, err := w.CreateFormFile("file", filename)
	if err != nil {
		return nil, fmt.Errorf("transcription: building form: %w", err)
	}
	if _, err := part.Write(audio); err != nil {
		return nil, fmt.Errorf("transcription: writing audio part: %w", err)
	}
	if err := w.WriteField("model", o.model); err != nil {
		return nil, err
	}
	if err := w.WriteField("response_format", "json"); err != nil {
		return nil, err
	}
	if lang := strings.TrimSpace(language); lang != "" {
		if err := w.WriteField("language", lang); err != nil {
			return nil, err
		}
	}
	if err := w.Close(); err != nil {
		return nil, fmt.Errorf("transcription: finalizing form: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.baseURL+transcribePath, &buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	if o.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+o.apiKey)
	}
	return req, nil
}

// parseTranscript handles both the OpenAI JSON shape ({"text":"…"}) and the
// plain-text body some self-hosted servers return, so the provider works
// across the whole ecosystem without per-server configuration.
func parseTranscript(contentType string, body []byte) (string, error) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return "", fmt.Errorf("transcription: empty response body")
	}
	if strings.Contains(contentType, "json") || trimmed[0] == '{' {
		var out struct {
			Text  string `json:"text"`
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(trimmed, &out); err != nil {
			return "", fmt.Errorf("transcription: decoding response: %w", err)
		}
		if msg := strings.TrimSpace(out.Error.Message); msg != "" {
			return "", fmt.Errorf("transcription: backend error: %s", msg)
		}
		return strings.TrimSpace(out.Text), nil
	}
	// Plain-text transcript (e.g. some whisper.cpp server configurations).
	return strings.TrimSpace(string(trimmed)), nil
}

// extensionForMime maps a media MIME type to a file extension the Whisper API
// recognizes. Telegram/WhatsApp voice notes are OGG/Opus; the rest cover the
// common formats. Unknown types default to .ogg (the dominant voice codec).
func extensionForMime(mime string) string {
	mime = strings.ToLower(mime)
	switch {
	case strings.Contains(mime, "ogg"), strings.Contains(mime, "opus"):
		return ".ogg"
	case strings.Contains(mime, "mpeg"), strings.Contains(mime, "mp3"):
		return ".mp3"
	case strings.Contains(mime, "wav"), strings.Contains(mime, "wave"):
		return ".wav"
	case strings.Contains(mime, "m4a"), strings.Contains(mime, "mp4"), strings.Contains(mime, "aac"):
		return ".m4a"
	case strings.Contains(mime, "flac"):
		return ".flac"
	case strings.Contains(mime, "webm"):
		return ".webm"
	default:
		return ".ogg"
	}
}

// snippet trims a response body for safe inclusion in an error message.
func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > maxErrorBodyToShow {
		return s[:maxErrorBodyToShow] + "…"
	}
	return s
}
