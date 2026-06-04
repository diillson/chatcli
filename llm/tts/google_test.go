/*
 * ChatCLI - Google Gemini TTS tests.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package tts

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/zap"
)

func TestGoogleTTS_WrapsPCMAsWAV(t *testing.T) {
	pcm := []byte{0x01, 0x02, 0x03, 0x04} // 4 bytes of fake PCM
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, ":generateContent") || r.URL.Query().Get("key") != "gkey" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		b64 := base64.StdEncoding.EncodeToString(pcm)
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"audio/L16;codec=pcm;rate=24000","data":"` + b64 + `"}}]}}]}`))
	}))
	defer srv.Close()

	g, err := NewGoogle("gkey", "", zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	g.baseURL = srv.URL
	audio, err := g.Synthesize(context.Background(), "hello", "Kore", "")
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if audio.Mime != "audio/wav" || audio.Ext != "wav" {
		t.Fatalf("expected wav, got %+v", audio)
	}
	// WAV header sanity: starts with RIFF....WAVE, data chunk = header(44)+pcm.
	if len(audio.Data) != 44+len(pcm) {
		t.Fatalf("expected %d bytes, got %d", 44+len(pcm), len(audio.Data))
	}
	if string(audio.Data[0:4]) != "RIFF" || string(audio.Data[8:12]) != "WAVE" {
		t.Fatalf("missing WAV header: %x", audio.Data[:12])
	}
}

func TestGoogleTTS_NoAudio(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"candidates":[]}`))
	}))
	defer srv.Close()
	g, _ := NewGoogle("k", "", zap.NewNop())
	g.baseURL = srv.URL
	if _, err := g.Synthesize(context.Background(), "x", "", ""); err == nil {
		t.Fatal("expected error when no audio returned")
	}
}

func TestSampleRateFromMime(t *testing.T) {
	if r := sampleRateFromMime("audio/L16;codec=pcm;rate=16000"); r != 16000 {
		t.Fatalf("rate = %d", r)
	}
	if r := sampleRateFromMime("audio/L16"); r != 24000 {
		t.Fatalf("default rate = %d", r)
	}
}

func TestFactory_GoogleTTSPin(t *testing.T) {
	for _, k := range []string{"CHATCLI_TTS_PROVIDER", "CHATCLI_TTS_CMD", "CHATCLI_TTS_URL", "OPENAI_API_KEY", "GROQ_API_KEY", "GOOGLEAI_API_KEY", "GEMINI_API_KEY", "GOOGLE_API_KEY"} {
		t.Setenv(k, "")
	}
	t.Setenv("CHATCLI_TTS_PROVIDER", "google")
	t.Setenv("GEMINI_API_KEY", "gkey")
	if _, ok := NewFromEnv(zap.NewNop()).(*Google); !ok {
		t.Fatal("expected Google TTS backend")
	}
}
