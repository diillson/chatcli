/*
 * ChatCLI - Outbound image-reply tests.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package gateway

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

// image attached → Telegram sendPhoto with the text as caption.
func TestTelegramSendPhoto(t *testing.T) {
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
		ChatID: "42", Text: "look at this",
		Image: &OutboundImage{Data: []byte("PNGDATA"), Mime: "image/png", FileName: "reply.png"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(path, "/sendPhoto") {
		t.Fatalf("expected sendPhoto, got path %q", path)
	}
	if !strings.HasPrefix(ctype, "multipart/form-data") {
		t.Fatalf("expected multipart, got %q", ctype)
	}
	if !strings.Contains(string(body), "PNGDATA") || !strings.Contains(string(body), "look at this") {
		t.Fatalf("multipart body missing image/caption")
	}
}

// photo send failure → falls back to text (sendMessage).
func TestTelegramSendPhoto_FallsBackToText(t *testing.T) {
	var hitPhoto, hitText bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/sendPhoto"):
			hitPhoto = true
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
		Image: &OutboundImage{Data: []byte("X"), Mime: "image/png", FileName: "r.png"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !hitPhoto || !hitText {
		t.Fatalf("expected photo attempt then text fallback (photo=%v text=%v)", hitPhoto, hitText)
	}
}

// image attached → Discord multipart upload with files[0] + payload_json.
func TestDiscordSendPhoto(t *testing.T) {
	var ctype string
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctype = r.Header.Get("Content-Type")
		body, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	a := NewDiscordAdapter("tok", zap.NewNop())
	a.restBase = srv.URL
	err := a.Send(context.Background(), OutboundMessage{
		ChatID: "chan1", Text: "discord caption",
		Image: &OutboundImage{Data: []byte("IMGBYTES"), Mime: "image/png", FileName: "reply.png"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(ctype, "multipart/form-data") {
		t.Fatalf("expected multipart, got %q", ctype)
	}
	if !strings.Contains(string(body), "IMGBYTES") || !strings.Contains(string(body), "discord caption") {
		t.Fatalf("multipart body missing image/content")
	}
	if !strings.Contains(string(body), "files[0]") {
		t.Fatalf("expected files[0] part")
	}
}

// image attached → Slack files.upload with the text as initial_comment.
func TestSlackSendPhoto(t *testing.T) {
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

	a := NewSlackAdapter("tok", "", ":0", "", zap.NewNop())
	a.apiBase = srv.URL
	err := a.Send(context.Background(), OutboundMessage{
		ChatID: "C123", Text: "slack caption",
		Image: &OutboundImage{Data: []byte("SLACKIMG"), Mime: "image/png", FileName: "reply.png"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(path, "/files.upload") {
		t.Fatalf("expected files.upload, got %q", path)
	}
	if !strings.HasPrefix(ctype, "multipart/form-data") {
		t.Fatalf("expected multipart, got %q", ctype)
	}
	if !strings.Contains(string(body), "SLACKIMG") || !strings.Contains(string(body), "slack caption") {
		t.Fatalf("multipart body missing image/initial_comment")
	}
}

// slack ok:false → falls back to chat.postMessage text.
func TestSlackSendPhoto_FallsBackToText(t *testing.T) {
	var hitUpload, hitText bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/files.upload"):
			hitUpload = true
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":false,"error":"not_authed"}`))
		case strings.Contains(r.URL.Path, "/chat.postMessage"):
			hitText = true
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))
		}
	}))
	defer srv.Close()

	a := NewSlackAdapter("tok", "", ":0", "", zap.NewNop())
	a.apiBase = srv.URL
	err := a.Send(context.Background(), OutboundMessage{
		ChatID: "C123", Text: "fallback",
		Image: &OutboundImage{Data: []byte("X"), Mime: "image/png"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !hitUpload || !hitText {
		t.Fatalf("expected upload attempt then text fallback (upload=%v text=%v)", hitUpload, hitText)
	}
}

// image attached → WhatsApp two-step upload (/media) then send (type:image).
func TestWhatsAppSendPhoto(t *testing.T) {
	var hitMedia, hitMessages bool
	var sendBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/media"):
			hitMedia = true
			if !strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
				t.Errorf("expected multipart media upload")
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":"MEDIA123"}`))
		case strings.HasSuffix(r.URL.Path, "/messages"):
			hitMessages = true
			sendBody, _ = io.ReadAll(r.Body)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer srv.Close()

	a := NewWhatsAppAdapter("tok", "PHONE", "", ":0", "", zap.NewNop())
	a.graphBase = srv.URL
	err := a.Send(context.Background(), OutboundMessage{
		ChatID: "5511", Text: "wa caption",
		Image: &OutboundImage{Data: []byte("WAIMG"), Mime: "image/png", FileName: "reply.png"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !hitMedia || !hitMessages {
		t.Fatalf("expected media upload then image message (media=%v messages=%v)", hitMedia, hitMessages)
	}
	var sent struct {
		Type  string `json:"type"`
		Image struct {
			ID      string `json:"id"`
			Caption string `json:"caption"`
		} `json:"image"`
	}
	if err := json.Unmarshal(sendBody, &sent); err != nil {
		t.Fatal(err)
	}
	if sent.Type != "image" || sent.Image.ID != "MEDIA123" || sent.Image.Caption != "wa caption" {
		t.Fatalf("unexpected image message: %s", sendBody)
	}
}

// whatsapp media upload failure → falls back to text message.
func TestWhatsAppSendPhoto_FallsBackToText(t *testing.T) {
	var hitMedia, hitText bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/media"):
			hitMedia = true
			w.WriteHeader(http.StatusBadRequest)
		case strings.HasSuffix(r.URL.Path, "/messages"):
			hitText = true
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer srv.Close()

	a := NewWhatsAppAdapter("tok", "PHONE", "", ":0", "", zap.NewNop())
	a.graphBase = srv.URL
	err := a.Send(context.Background(), OutboundMessage{
		ChatID: "5511", Text: "fallback",
		Image: &OutboundImage{Data: []byte("X"), Mime: "image/png"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !hitMedia || !hitText {
		t.Fatalf("expected media attempt then text fallback (media=%v text=%v)", hitMedia, hitText)
	}
}

// image attached → webhook JSON carries image_b64 alongside the text.
func TestWebhookSendPhoto(t *testing.T) {
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	a := NewWebhookAdapter(":0", "", "", srv.URL, zap.NewNop())
	err := a.Send(context.Background(), OutboundMessage{
		ChatID: "abc", Text: "webhook caption",
		Image: &OutboundImage{Data: []byte("HOOKIMG"), Mime: "image/png", FileName: "reply.png"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]string
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	if out["text"] != "webhook caption" {
		t.Fatalf("text not preserved: %q", out["text"])
	}
	if out["image_b64"] == "" || out["image_mime"] != "image/png" || out["image_filename"] != "reply.png" {
		t.Fatalf("missing image fields: %s", body)
	}
}
