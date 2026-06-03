/*
 * ChatCLI - OpenAI-compatible TTS tests.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package tts

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/zap"
)

func TestOpenAICompatible_Synthesize(t *testing.T) {
	var gotBody map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/audio/speech") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = w.Write([]byte("FAKEAUDIODATA"))
	}))
	defer srv.Close()

	p, err := NewOpenAICompatible(srv.URL, "", "tts-1", "selfhosted", zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	audio, err := p.Synthesize(context.Background(), "hello world", "alloy", "mp3")
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if string(audio.Data) != "FAKEAUDIODATA" || audio.Mime != "audio/mpeg" || audio.Ext != "mp3" {
		t.Fatalf("unexpected audio %+v", audio)
	}
	if gotBody["input"] != "hello world" || gotBody["voice"] != "alloy" {
		t.Fatalf("request body wrong: %v", gotBody)
	}
}

func TestOpenAICompatible_ErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("bad voice"))
	}))
	defer srv.Close()
	p, _ := NewOpenAICompatible(srv.URL, "k", "tts-1", "openai", zap.NewNop())
	if _, err := p.Synthesize(context.Background(), "x", "", "mp3"); err == nil {
		t.Fatal("expected error on 400")
	}
}

func TestOpenAICompatible_BadURL(t *testing.T) {
	if _, err := NewOpenAICompatible("ftp://nope", "", "", "x", zap.NewNop()); err == nil {
		t.Fatal("expected error for non-http url")
	}
}
