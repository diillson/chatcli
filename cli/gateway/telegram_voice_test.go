/*
 * ChatCLI - Telegram voice-reply tests.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package gateway

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/zap"
)

// ogg audio → Telegram sendVoice with the text as caption.
func TestTelegramSendVoice_OGG(t *testing.T) {
	var path, ctype string
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		ctype = r.Header.Get("Content-Type")
		body, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	a := NewTelegramAdapter("tok", nil, zap.NewNop())
	a.baseURL = srv.URL
	err := a.Send(context.Background(), OutboundMessage{
		ChatID: "42", Text: "spoken caption",
		Audio: &OutboundAudio{Data: []byte("OGGDATA"), Mime: "audio/ogg", FileName: "reply.ogg"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(path, "/sendVoice") {
		t.Fatalf("expected sendVoice, got path %q", path)
	}
	if !strings.HasPrefix(ctype, "multipart/form-data") {
		t.Fatalf("expected multipart, got %q", ctype)
	}
	if !strings.Contains(string(body), "OGGDATA") || !strings.Contains(string(body), "spoken caption") {
		t.Fatalf("multipart body missing audio/caption")
	}
}

// non-ogg audio → sendAudio.
func TestTelegramSendVoice_MP3UsesSendAudio(t *testing.T) {
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	a := NewTelegramAdapter("tok", nil, zap.NewNop())
	a.baseURL = srv.URL
	err := a.Send(context.Background(), OutboundMessage{
		ChatID: "42", Text: "",
		Audio: &OutboundAudio{Data: []byte("MP3"), Mime: "audio/mpeg", FileName: "reply.mp3"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(path, "/sendAudio") {
		t.Fatalf("expected sendAudio, got %q", path)
	}
}

// voice send failure → falls back to text (sendMessage).
func TestTelegramSendVoice_FallsBackToText(t *testing.T) {
	var hitVoice, hitText bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/sendVoice"):
			hitVoice = true
			w.WriteHeader(http.StatusBadRequest)
		case strings.Contains(r.URL.Path, "/sendMessage"):
			hitText = true
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))
		}
	}))
	defer srv.Close()

	a := NewTelegramAdapter("tok", nil, zap.NewNop())
	a.baseURL = srv.URL
	err := a.Send(context.Background(), OutboundMessage{
		ChatID: "42", Text: "fallback text",
		Audio: &OutboundAudio{Data: []byte("X"), Mime: "audio/ogg", FileName: "r.ogg"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !hitVoice || !hitText {
		t.Fatalf("expected voice attempt then text fallback (voice=%v text=%v)", hitVoice, hitText)
	}
}
