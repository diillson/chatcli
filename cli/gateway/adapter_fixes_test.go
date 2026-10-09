/*
 * ChatCLI - Gateway adapter fixes: Discord long replies, Slack ok:false,
 * fail-closed WhatsApp verification, audio-capable channels.
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
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"go.uber.org/zap"
)

func TestSplitDiscordContent(t *testing.T) {
	if got := splitDiscordContent("", 2000); len(got) != 1 || got[0] != "" {
		t.Fatalf("empty text = %q", got)
	}
	if got := splitDiscordContent("short", 2000); len(got) != 1 || got[0] != "short" {
		t.Fatalf("short text = %q", got)
	}

	// Lines break on the newline, never inside a line.
	line := strings.Repeat("a", 900) + "\n"
	text := line + line + line // 2703 chars
	parts := splitDiscordContent(text, 2000)
	if len(parts) != 2 || parts[0] != line+line || parts[1] != line {
		t.Fatalf("newline split = %d parts %v", len(parts), lens(parts))
	}

	// With no newline, words break on the space.
	words := strings.Repeat("word ", 500) // 2500 chars
	parts = splitDiscordContent(words, 2000)
	if strings.Join(parts, "") != words {
		t.Fatal("split lost text")
	}
	for _, p := range parts[:len(parts)-1] {
		if !strings.HasSuffix(p, " ") {
			t.Fatalf("part does not end on a space: %q", p[len(p)-10:])
		}
	}

	// A single unbroken run is hard-cut, counted in characters, not bytes.
	run := strings.Repeat("é", 4500)
	parts = splitDiscordContent(run, 2000)
	if len(parts) != 3 || strings.Join(parts, "") != run {
		t.Fatalf("hard cut = %v", lens(parts))
	}
	for _, p := range parts {
		if n := utf8.RuneCountInString(p); n > 2000 {
			t.Fatalf("part has %d characters", n)
		}
	}
}

func lens(parts []string) []int {
	out := make([]int, len(parts))
	for i, p := range parts {
		out[i] = utf8.RuneCountInString(p)
	}
	return out
}

// A reply over Discord's 2000-character limit used to go out as one message
// that Discord rejects; it now arrives as several, in order, none too long.
func TestDiscordSend_LongReplyIsChunkedInOrder(t *testing.T) {
	var mu sync.Mutex
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var p map[string]string
		_ = json.Unmarshal(b, &p)
		if utf8.RuneCountInString(p["content"]) > discordMaxContent {
			w.WriteHeader(http.StatusBadRequest) // what Discord does
			return
		}
		mu.Lock()
		got = append(got, p["content"])
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	d := NewDiscordAdapter("tok", zap.NewNop())
	d.restBase = srv.URL
	para := strings.Repeat("x", 1200) + "\n"
	text := para + para + para // 3603 chars
	if err := d.Send(context.Background(), OutboundMessage{ChatID: "c1", Text: text}); err != nil {
		t.Fatal(err)
	}
	if len(got) < 2 || strings.Join(got, "") != text {
		t.Fatalf("delivered %d parts %v; want the whole text in order", len(got), lens(got))
	}
}

// With an image, the first part rides on the upload and the rest follow.
func TestDiscordSend_LongReplyWithImage(t *testing.T) {
	var photoContent string
	var texts []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
			_ = r.ParseMultipartForm(1 << 20)
			var p map[string]string
			_ = json.Unmarshal([]byte(r.FormValue("payload_json")), &p)
			photoContent = p["content"]
		} else {
			b, _ := io.ReadAll(r.Body)
			var p map[string]string
			_ = json.Unmarshal(b, &p)
			texts = append(texts, p["content"])
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	d := NewDiscordAdapter("tok", zap.NewNop())
	d.restBase = srv.URL
	text := strings.Repeat("y ", 1500) // 3000 chars
	err := d.Send(context.Background(), OutboundMessage{
		ChatID: "c1", Text: text,
		Image: &OutboundImage{Data: []byte("IMG"), Mime: "image/png"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if utf8.RuneCountInString(photoContent) > discordMaxContent || photoContent == "" {
		t.Fatalf("photo content has %d characters", utf8.RuneCountInString(photoContent))
	}
	if photoContent+strings.Join(texts, "") != text {
		t.Fatal("photo caption plus follow-ups do not add up to the reply")
	}
}

// Slack answers 200 with ok:false on failures such as not_in_channel; that
// used to count as delivered.
func TestSlackSend_OKFalseIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":false,"error":"not_in_channel"}`))
	}))
	defer srv.Close()

	a := NewSlackAdapter("tok", "", ":0", "", zap.NewNop())
	a.apiBase = srv.URL
	err := a.Send(context.Background(), OutboundMessage{ChatID: "C1", Text: "hi"})
	if err == nil || !strings.Contains(err.Error(), "not_in_channel") {
		t.Fatalf("err = %v, want the Slack error", err)
	}
}

// An unset verify token used to let a handshake with an empty
// hub.verify_token through.
func TestWhatsAppHandshake_UnsetVerifyTokenFailsClosed(t *testing.T) {
	a := NewWhatsAppAdapter("tok", "phone", "", ":0", "/wa", zap.NewNop())
	srv := httptest.NewServer(a.webhookHandler(context.Background(), make(chan InboundMessage, 1)))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "?hub.mode=subscribe&hub.verify_token=&hub.challenge=42")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden || string(body) == "42" {
		t.Fatalf("status %d body %q; want 403 and no echo", resp.StatusCode, body)
	}
}

// Only adapters that deliver audio are audio repliers, and the name table the
// agent loop reads agrees with the adapters.
func TestAudioReplyCapabilityMatchesAdapters(t *testing.T) {
	adapters := []Adapter{
		NewTelegramAdapter("t", nil, zap.NewNop()),
		NewSlackAdapter("t", "s", ":0", "", zap.NewNop()),
		NewDiscordAdapter("t", zap.NewNop()),
		NewWhatsAppAdapter("t", "p", "v", ":0", "", zap.NewNop()),
		NewWebhookAdapter(":0", "", "s", "", zap.NewNop()),
	}
	for _, a := range adapters {
		if adapterRepliesWithAudio(a) != PlatformRepliesWithAudio(a.Name()) {
			t.Errorf("%s: adapter capability %v, platform table %v",
				a.Name(), adapterRepliesWithAudio(a), PlatformRepliesWithAudio(a.Name()))
		}
	}
	if !PlatformRepliesWithAudio(telegramPlatform) {
		t.Error("telegram must reply with audio")
	}
	for _, p := range []string{slackPlatform, discordPlatform, whatsappPlatform, webhookPlatform} {
		if PlatformRepliesWithAudio(p) {
			t.Errorf("%s does not deliver audio", p)
		}
	}
}

// textOnlyAdapter records sends and, like Slack/Discord/WhatsApp/webhook,
// does not deliver audio.
type textOnlyAdapter struct {
	mu   sync.Mutex
	sent []OutboundMessage
}

func (*textOnlyAdapter) Name() string                                       { return "textonly" }
func (*textOnlyAdapter) Start(context.Context, chan<- InboundMessage) error { return nil }
func (a *textOnlyAdapter) Send(_ context.Context, m OutboundMessage) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sent = append(a.sent, m)
	return nil
}
func (a *textOnlyAdapter) finals() []OutboundMessage {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]OutboundMessage(nil), a.sent...)
}

// A channel that cannot deliver audio never pays for synthesis.
func TestRunner_NoSynthesisForTextOnlyChannel(t *testing.T) {
	rec := &textOnlyAdapter{}
	agent := func(_ context.Context, _ string, _ string) (string, error) { return "the answer", nil }
	r := NewRunner([]Adapter{rec}, agent, zap.NewNop(), 1)
	r.thinkingDelay = time.Hour
	synthesized := 0
	r.SetVoiceSynthesizer(func(_ context.Context, _ string) *OutboundAudio {
		synthesized++
		return &OutboundAudio{Data: []byte("A"), Mime: "audio/ogg"}
	})
	r.SetVoiceMode(VoiceModeAlways)

	r.handle(context.Background(), InboundMessage{Platform: "textonly", ChatID: "1", Text: "hi", Audio: &InboundAudio{Data: []byte("x")}})

	if synthesized != 0 {
		t.Fatalf("synthesized %d clips for a channel that drops audio", synthesized)
	}
	for _, m := range rec.finals() {
		if m.Audio != nil {
			t.Fatal("audio attached on a text-only channel")
		}
	}
}
