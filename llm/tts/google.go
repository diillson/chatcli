/*
 * ChatCLI - Google Gemini TTS provider.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Gemini's TTS does NOT speak the OpenAI /audio/speech shape, so it needs its
 * own backend: POST {base}/v1beta/models/{model}:generateContent?key=KEY with
 * responseModalities:["AUDIO"], returning base64 PCM (s16le, mono) in
 * candidates[0].content.parts[].inlineData. We wrap that PCM in a WAV header so
 * the result is a normal playable file.
 */
package tts

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/diillson/chatcli/utils"
	"go.uber.org/zap"
)

const (
	googleTTSBase      = "https://generativelanguage.googleapis.com"
	defaultGeminiTTS   = "gemini-2.5-flash-preview-tts"
	defaultGeminiVoice = "Kore"
	defaultPCMSampleHz = 24000
	maxGoogleErrBody   = 300
)

// Google synthesizes speech via Gemini's generateContent AUDIO modality.
type Google struct {
	baseURL string
	apiKey  string
	model   string
	client  *http.Client
}

// NewGoogle builds the provider. apiKey is required (the user's own key).
func NewGoogle(apiKey, model string, logger *zap.Logger) (*Google, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, fmt.Errorf("tts: Google API key is required")
	}
	if strings.TrimSpace(model) == "" {
		model = defaultGeminiTTS
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Google{baseURL: googleTTSBase, apiKey: strings.TrimSpace(apiKey), model: model, client: utils.NewHTTPClient(logger, synthTimeout)}, nil
}

// Name returns "google".
func (*Google) Name() string { return "google" }

// Synthesize posts the text and wraps the returned PCM as WAV.
func (g *Google) Synthesize(ctx context.Context, text, voice, _ string) (Audio, error) {
	if strings.TrimSpace(text) == "" {
		return Audio{}, fmt.Errorf("tts: empty text")
	}
	if strings.TrimSpace(voice) == "" {
		voice = defaultGeminiVoice
	}

	reqBody := map[string]interface{}{
		"contents": []map[string]interface{}{
			{"parts": []map[string]string{{"text": text}}},
		},
		"generationConfig": map[string]interface{}{
			"responseModalities": []string{"AUDIO"},
			"speechConfig": map[string]interface{}{
				"voiceConfig": map[string]interface{}{
					"prebuiltVoiceConfig": map[string]string{"voiceName": voice},
				},
			},
		},
	}
	body, _ := json.Marshal(reqBody)
	endpoint := fmt.Sprintf("%s/v1beta/models/%s:generateContent?key=%s", g.baseURL, g.model, g.apiKey)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return Audio{}, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := g.client.Do(req)
	if err != nil {
		return Audio{}, fmt.Errorf("tts: request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, maxGoogleErrBody))
		return Audio{}, fmt.Errorf("tts: google returned %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
	}

	var out struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					InlineData struct {
						MimeType string `json:"mimeType"`
						Data     string `json:"data"`
					} `json:"inlineData"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return Audio{}, fmt.Errorf("tts: decode response: %w", err)
	}

	for _, c := range out.Candidates {
		for _, p := range c.Content.Parts {
			if p.InlineData.Data == "" {
				continue
			}
			pcm, derr := base64.StdEncoding.DecodeString(p.InlineData.Data)
			if derr != nil || len(pcm) == 0 {
				continue
			}
			// Audio is already WAV/another container on some responses; pass
			// through unless it's raw PCM (audio/L16;...), which we must wrap.
			if isRawPCM(p.InlineData.MimeType) {
				wav := pcmToWAV(pcm, sampleRateFromMime(p.InlineData.MimeType))
				return Audio{Data: wav, Mime: "audio/wav", Ext: "wav"}, nil
			}
			return Audio{Data: pcm, Mime: p.InlineData.MimeType, Ext: "wav"}, nil
		}
	}
	return Audio{}, fmt.Errorf("tts: google returned no audio")
}

// isRawPCM reports whether the mime denotes uncompressed PCM that needs a WAV
// container (e.g. "audio/L16;codec=pcm;rate=24000").
func isRawPCM(mime string) bool {
	m := strings.ToLower(mime)
	return strings.Contains(m, "l16") || strings.Contains(m, "pcm")
}

// sampleRateFromMime parses "rate=NNNNN" from the mime, defaulting to 24000.
func sampleRateFromMime(mime string) int {
	for _, part := range strings.Split(mime, ";") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, "rate=") {
			if v, err := strconv.Atoi(strings.TrimPrefix(part, "rate=")); err == nil && v > 0 {
				return v
			}
		}
	}
	return defaultPCMSampleHz
}

// pcmToWAV wraps signed-16-bit little-endian mono PCM in a 44-byte WAV header.
// WAV size fields are 32-bit; safeU32 clamps the (always tiny) TTS sizes so the
// int→uint32 conversions are provably overflow-free.
func pcmToWAV(pcm []byte, sampleRate int) []byte {
	const (
		numChannels   = 1
		bitsPerSample = 16
	)
	dataLen := safeU32(len(pcm))
	rate := safeU32(sampleRate)
	byteRate := safeU32(sampleRate * numChannels * bitsPerSample / 8)

	var b bytes.Buffer
	b.WriteString("RIFF")
	_ = binary.Write(&b, binary.LittleEndian, dataLen+36)
	b.WriteString("WAVE")
	b.WriteString("fmt ")
	_ = binary.Write(&b, binary.LittleEndian, uint32(16))
	_ = binary.Write(&b, binary.LittleEndian, uint16(1)) // PCM
	_ = binary.Write(&b, binary.LittleEndian, uint16(numChannels))
	_ = binary.Write(&b, binary.LittleEndian, rate)
	_ = binary.Write(&b, binary.LittleEndian, byteRate)
	_ = binary.Write(&b, binary.LittleEndian, uint16(numChannels*bitsPerSample/8)) // block align
	_ = binary.Write(&b, binary.LittleEndian, uint16(bitsPerSample))
	b.WriteString("data")
	_ = binary.Write(&b, binary.LittleEndian, dataLen)
	b.Write(pcm)
	return b.Bytes()
}

// safeU32 converts a non-negative int to uint32 with explicit bounds, so the
// conversion is provably overflow-free (satisfies gosec G115).
func safeU32(n int) uint32 {
	if n < 0 {
		return 0
	}
	if n > math.MaxUint32 {
		return math.MaxUint32
	}
	return uint32(n)
}
