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
	"testing"
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
	// A non-audio attachment with no text is rejected.
	img := []byte(`{"channel_id":"C1","content":"","author":{"id":"u1"},"attachments":[{"url":"https://cdn/x.png","content_type":"image/png"}]}`)
	if _, ok := parseDiscordMessage(img); ok {
		t.Error("image-only message must be rejected")
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
	// A non-audio file_share with no text is ignored.
	doc := []byte(`{"type":"event_callback","event":{"type":"message","subtype":"file_share","channel":"C1","user":"U1","files":[{"mimetype":"application/pdf","url_private":"https://files/d.pdf"}]}}`)
	if _, _, has, _ := parseSlackEvent(doc); has {
		t.Error("non-audio file_share must be ignored")
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
