/*
 * ChatCLI - tests for inbound voice/audio handling across adapters.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package gateway

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/zap"
)

func TestMaxAudioBytes(t *testing.T) {
	t.Setenv("CHATCLI_GATEWAY_MAX_AUDIO_BYTES", "")
	if got := maxAudioBytes(); got != defaultMaxAudioBytes {
		t.Errorf("default = %d, want %d", got, defaultMaxAudioBytes)
	}
	t.Setenv("CHATCLI_GATEWAY_MAX_AUDIO_BYTES", "1234")
	if got := maxAudioBytes(); got != 1234 {
		t.Errorf("override = %d, want 1234", got)
	}
	t.Setenv("CHATCLI_GATEWAY_MAX_AUDIO_BYTES", "garbage")
	if got := maxAudioBytes(); got != defaultMaxAudioBytes {
		t.Errorf("garbage should keep default, got %d", got)
	}
}

func TestFetchAudioBytes(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "audio/ogg")
		_, _ = w.Write([]byte("0123456789"))
	}))
	defer srv.Close()

	data, ctype, err := fetchAudioBytes(context.Background(), srv.Client(), srv.URL, "tok", 100)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "0123456789" || ctype != "audio/ogg" {
		t.Errorf("data=%q ctype=%q", data, ctype)
	}
	if gotAuth != "Bearer tok" {
		t.Errorf("Authorization = %q", gotAuth)
	}

	// Over the cap.
	if _, _, err := fetchAudioBytes(context.Background(), srv.Client(), srv.URL, "", 5); err == nil {
		t.Error("expected size-cap error")
	}

	// Non-200.
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer bad.Close()
	if _, _, err := fetchAudioBytes(context.Background(), bad.Client(), bad.URL, "", 100); err == nil {
		t.Error("expected status error")
	}
}

func TestParseTelegram_Voice(t *testing.T) {
	body := []byte(`{"ok":true,"result":[{"update_id":7,"message":{"chat":{"id":42},"from":{"id":9,"username":"ed"},"voice":{"file_id":"VOICE123","mime_type":"audio/ogg"},"caption":"see this"}}]}`)
	msgs, _, err := parseTelegramUpdates(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("want 1 msg, got %d", len(msgs))
	}
	m := msgs[0]
	if m.Audio == nil || m.Audio.ref != "VOICE123" || m.Audio.MimeType != "audio/ogg" {
		t.Errorf("audio = %+v", m.Audio)
	}
	if m.Text != "see this" { // caption promoted to text
		t.Errorf("text = %q, want caption", m.Text)
	}
}

func TestParseTelegram_Photo(t *testing.T) {
	// A photo arrives as an ascending-size array; the largest (last) is picked,
	// the caption is promoted to text, and an image-only message is kept.
	body := []byte(`{"ok":true,"result":[{"update_id":8,"message":{"chat":{"id":42},"from":{"id":9,"username":"ed"},"caption":"look","photo":[{"file_id":"SMALL"},{"file_id":"BIG","file_size":9000}]}}]}`)
	msgs, _, err := parseTelegramUpdates(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("want 1 msg, got %d", len(msgs))
	}
	m := msgs[0]
	if m.Image == nil || m.Image.ref != "BIG" {
		t.Errorf("image = %+v", m.Image)
	}
	if m.Text != "look" { // caption promoted to text
		t.Errorf("text = %q, want caption", m.Text)
	}
	// An image document is also picked up when its mime is an image type.
	docBody := []byte(`{"ok":true,"result":[{"update_id":9,"message":{"chat":{"id":42},"from":{"id":9},"document":{"file_id":"DOC1","mime_type":"image/png","file_name":"x.png"}}}]}`)
	dmsgs, _, err := parseTelegramUpdates(docBody)
	if err != nil || len(dmsgs) != 1 {
		t.Fatalf("image document should parse: err=%v n=%d", err, len(dmsgs))
	}
	if dmsgs[0].Image == nil || dmsgs[0].Image.ref != "DOC1" || dmsgs[0].Image.MimeType != "image/png" {
		t.Errorf("document image = %+v", dmsgs[0].Image)
	}
}

func TestParseWhatsApp_Audio(t *testing.T) {
	body := []byte(`{"entry":[{"changes":[{"value":{"messages":[{"from":"55119","type":"audio","audio":{"id":"MID9","mime_type":"audio/ogg"}}]}}]}]}`)
	msgs := parseWhatsAppInbound(body)
	if len(msgs) != 1 {
		t.Fatalf("want 1, got %d", len(msgs))
	}
	if msgs[0].Audio == nil || msgs[0].Audio.ref != "MID9" {
		t.Errorf("audio = %+v", msgs[0].Audio)
	}
}

func TestParseDiscord_AudioAttachment(t *testing.T) {
	d := []byte(`{"channel_id":"C1","content":"","author":{"id":"u1"},"attachments":[{"url":"https://cdn/x.ogg","content_type":"audio/ogg","filename":"x.ogg"}]}`)
	m, ok := parseDiscordMessage(d)
	if !ok {
		t.Fatal("audio-only message should be accepted")
	}
	if m.Audio == nil || m.Audio.ref != "https://cdn/x.ogg" || m.Audio.FileName != "x.ogg" {
		t.Errorf("audio = %+v", m.Audio)
	}
	// An image-only attachment with no text is now ACCEPTED and carries an
	// InboundImage (vision input).
	img := []byte(`{"channel_id":"C1","content":"","author":{"id":"u1"},"attachments":[{"url":"https://cdn/x.png","content_type":"image/png","filename":"x.png"}]}`)
	im, ok := parseDiscordMessage(img)
	if !ok {
		t.Fatal("image-only message must now be accepted")
	}
	if im.Image == nil || im.Image.ref != "https://cdn/x.png" || im.Image.MimeType != "image/png" {
		t.Errorf("image = %+v", im.Image)
	}
	// A non-image, non-audio attachment with no text is still rejected.
	doc := []byte(`{"channel_id":"C1","content":"","author":{"id":"u1"},"attachments":[{"url":"https://cdn/x.pdf","content_type":"application/pdf"}]}`)
	if _, ok := parseDiscordMessage(doc); ok {
		t.Error("non-media attachment with no text must be rejected")
	}
}

func TestParseSlack_AudioFile(t *testing.T) {
	body := []byte(`{"type":"event_callback","event":{"type":"message","subtype":"file_share","channel":"C1","user":"U1","files":[{"mimetype":"audio/mp4","url_private":"https://files/a.m4a","name":"a.m4a"}]}}`)
	_, msg, has, err := parseSlackEvent(body)
	if err != nil || !has {
		t.Fatalf("audio file_share should yield a message (has=%v err=%v)", has, err)
	}
	if msg.Audio == nil || msg.Audio.ref != "https://files/a.m4a" {
		t.Errorf("audio = %+v", msg.Audio)
	}
	// A non-media file_share (e.g. a PDF) with no text is ignored.
	doc := []byte(`{"type":"event_callback","event":{"type":"message","subtype":"file_share","channel":"C1","user":"U1","files":[{"mimetype":"application/pdf","url_private":"https://files/d.pdf"}]}}`)
	if _, _, has, _ := parseSlackEvent(doc); has {
		t.Error("non-media file_share must be ignored")
	}
	// An image file_share with no text is now ACCEPTED and carries an InboundImage.
	img := []byte(`{"type":"event_callback","event":{"type":"message","subtype":"file_share","channel":"C1","user":"U1","files":[{"mimetype":"image/png","url_private":"https://files/p.png","name":"p.png"}]}}`)
	_, imsg, has, err := parseSlackEvent(img)
	if err != nil || !has {
		t.Fatalf("image file_share should yield a message (has=%v err=%v)", has, err)
	}
	if imsg.Image == nil || imsg.Image.ref != "https://files/p.png" || imsg.Image.MimeType != "image/png" {
		t.Errorf("slack image = %+v", imsg.Image)
	}
}

func TestParseWebhook_Audio(t *testing.T) {
	// Inline base64 audio is decoded in-place.
	b64 := base64.StdEncoding.EncodeToString([]byte("voice-bytes"))
	m, ok := parseWebhookInbound([]byte(`{"chat_id":"c1","audio_b64":"` + b64 + `","audio_mime":"audio/ogg"}`))
	if !ok || m.Audio == nil || string(m.Audio.Data) != "voice-bytes" {
		t.Errorf("b64 decode failed: ok=%v audio=%+v", ok, m.Audio)
	}
	// audio_url is recorded for later fetch.
	m, ok = parseWebhookInbound([]byte(`{"chat_id":"c1","audio_url":"https://x/a.ogg"}`))
	if !ok || m.Audio == nil || m.Audio.ref != "https://x/a.ogg" {
		t.Errorf("audio_url not recorded: %+v", m.Audio)
	}
	// Inline base64 image is decoded in-place; an image-only payload is accepted.
	imgb64 := base64.StdEncoding.EncodeToString([]byte("png-bytes"))
	m, ok = parseWebhookInbound([]byte(`{"chat_id":"c1","image_b64":"` + imgb64 + `","image_mime":"image/png"}`))
	if !ok || m.Image == nil || string(m.Image.Data) != "png-bytes" || m.Image.MimeType != "image/png" {
		t.Errorf("image b64 decode failed: ok=%v image=%+v", ok, m.Image)
	}
	// image_url is recorded for later fetch.
	m, ok = parseWebhookInbound([]byte(`{"chat_id":"c1","image_url":"https://x/p.png"}`))
	if !ok || m.Image == nil || m.Image.ref != "https://x/p.png" {
		t.Errorf("image_url not recorded: %+v", m.Image)
	}
	// Text-only still works; empty payload rejected.
	if _, ok := parseWebhookInbound([]byte(`{"chat_id":"c1","text":"hi"}`)); !ok {
		t.Error("text-only message must still parse")
	}
	if _, ok := parseWebhookInbound([]byte(`{"chat_id":"c1"}`)); ok {
		t.Error("empty message must be rejected")
	}
	// Bad base64 is rejected.
	if _, ok := parseWebhookInbound([]byte(`{"chat_id":"c1","audio_b64":"!!!notb64"}`)); ok {
		t.Error("invalid base64 must be rejected")
	}
}

func TestIsAudioMime(t *testing.T) {
	for _, m := range []string{"audio/ogg", "AUDIO/MP4", " audio/mpeg "} {
		if !isAudioMime(m) {
			t.Errorf("%q should be audio", m)
		}
	}
	for _, m := range []string{"image/png", "", "video/mp4"} {
		if isAudioMime(m) {
			t.Errorf("%q should not be audio", m)
		}
	}
}

func TestTelegramDownloadFile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/getFile"):
			_, _ = w.Write([]byte(`{"ok":true,"result":{"file_path":"voice/f9.ogg"}}`))
		case strings.Contains(r.URL.Path, "/file/"):
			w.Header().Set("Content-Type", "audio/ogg")
			_, _ = w.Write([]byte("OGG-BYTES"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	ad := NewTelegramAdapter("TOKEN", nil, zap.NewNop())
	ad.baseURL = srv.URL
	data, mime, err := ad.downloadFile(context.Background(), "FID", maxAudioBytes())
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "OGG-BYTES" || mime != "audio/ogg" {
		t.Errorf("data=%q mime=%q", data, mime)
	}
}

func TestWhatsAppDownloadMedia(t *testing.T) {
	var base string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer TOK" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/dl") {
			w.Header().Set("Content-Type", "audio/ogg")
			_, _ = w.Write([]byte("WA-AUDIO"))
			return
		}
		// media-id lookup → returns the download URL
		_, _ = w.Write([]byte(`{"url":"` + base + `/dl","mime_type":"audio/ogg"}`))
	}))
	defer srv.Close()
	base = srv.URL

	ad := NewWhatsAppAdapter("TOK", "PHONE", "verify", "", "", zap.NewNop())
	ad.graphBase = srv.URL
	data, mime, err := ad.downloadMedia(context.Background(), "MID", maxAudioBytes())
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "WA-AUDIO" || mime != "audio/ogg" {
		t.Errorf("data=%q mime=%q", data, mime)
	}
}

func TestSlackHydrateAudio_Bearer(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "audio/mp4")
		_, _ = w.Write([]byte("SLACK-AUDIO"))
	}))
	defer srv.Close()

	ad := NewSlackAdapter("xoxb-tok", "", "", "", zap.NewNop())
	msg := InboundMessage{Audio: &InboundAudio{ref: srv.URL}}
	ad.hydrateAudio(context.Background(), &msg)
	if msg.Audio == nil || string(msg.Audio.Data) != "SLACK-AUDIO" {
		t.Fatalf("audio not hydrated: %+v", msg.Audio)
	}
	if gotAuth != "Bearer xoxb-tok" {
		t.Errorf("slack download must send the bot token; got %q", gotAuth)
	}
}

func TestDiscordHydrateAudio(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "audio/ogg")
		_, _ = w.Write([]byte("DISCORD-AUDIO"))
	}))
	defer srv.Close()

	ad := NewDiscordAdapter("tok", zap.NewNop())
	msg := InboundMessage{Audio: &InboundAudio{ref: srv.URL}}
	ad.hydrateAudio(context.Background(), &msg)
	if msg.Audio == nil || string(msg.Audio.Data) != "DISCORD-AUDIO" {
		t.Fatalf("audio not hydrated: %+v", msg.Audio)
	}
}

func TestWebhookHydrateAudio(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "audio/ogg")
		_, _ = w.Write([]byte("WH-AUDIO"))
	}))
	defer srv.Close()

	ad := NewWebhookAdapter("", "", "", "", zap.NewNop())
	msg := InboundMessage{Audio: &InboundAudio{ref: srv.URL}}
	ad.hydrateAudio(context.Background(), &msg)
	if msg.Audio == nil || string(msg.Audio.Data) != "WH-AUDIO" {
		t.Fatalf("audio not hydrated: %+v", msg.Audio)
	}
	// A failed download clears the attachment.
	bad := InboundMessage{Audio: &InboundAudio{ref: "http://127.0.0.1:0/nope"}}
	ad.hydrateAudio(context.Background(), &bad)
	if bad.Audio != nil {
		t.Error("a failed download must clear Audio")
	}
}
