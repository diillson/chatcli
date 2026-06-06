/*
 * ChatCLI - Gateway voice-reply tests.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package gateway

import (
	"context"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
)

// recordingAdapter captures the OutboundMessages it is asked to send.
type recordingAdapter struct {
	mu   sync.Mutex
	sent []OutboundMessage
}

func (*recordingAdapter) Name() string                                       { return "rec" }
func (*recordingAdapter) Start(context.Context, chan<- InboundMessage) error { return nil }
func (a *recordingAdapter) Send(_ context.Context, m OutboundMessage) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sent = append(a.sent, m)
	return nil
}
func (a *recordingAdapter) finals() []OutboundMessage {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]OutboundMessage, len(a.sent))
	copy(out, a.sent)
	return out
}

func newVoiceRunner(t *testing.T, rec *recordingAdapter, reply string) *Runner {
	t.Helper()
	agent := func(_ context.Context, _ string, _ string) (string, error) { return reply, nil }
	r := NewRunner([]Adapter{rec}, agent, zap.NewNop(), 1)
	r.thinkingDelay = time.Hour // suppress the working notice in tests
	return r
}

// finalReply returns the outbound message whose text matches reply, or nil.
func finalReply(rec *recordingAdapter, reply string) *OutboundMessage {
	for _, m := range rec.finals() {
		if m.Text == reply {
			final := m
			return &final
		}
	}
	return nil
}

// setEchoSynthesizer attaches a synthesizer that wraps the reply text so tests
// can assert exactly which text reached synthesis.
func setEchoSynthesizer(r *Runner) {
	r.SetVoiceSynthesizer(func(_ context.Context, text string) *OutboundAudio {
		return &OutboundAudio{Data: []byte("AUDIO:" + text), Mime: "audio/ogg", FileName: "reply.ogg"}
	})
}

func TestRunner_InKindVoiceMessageGetsAudio(t *testing.T) {
	rec := &recordingAdapter{}
	r := newVoiceRunner(t, rec, "the answer")
	setEchoSynthesizer(r) // default mode: in-kind

	r.handle(context.Background(), InboundMessage{
		Platform: "rec", ChatID: "1", Text: "hi",
		Audio: &InboundAudio{Data: []byte("voice-note"), MimeType: "audio/ogg"},
	})

	final := finalReply(rec, "the answer")
	if final == nil {
		t.Fatal("no final reply sent")
	}
	if final.Audio == nil || string(final.Audio.Data) != "AUDIO:the answer" {
		t.Fatalf("expected audio attached, got %+v", final.Audio)
	}
}

func TestRunner_InKindTextMessageStaysTextOnly(t *testing.T) {
	rec := &recordingAdapter{}
	r := newVoiceRunner(t, rec, "the answer")
	setEchoSynthesizer(r) // default mode: in-kind

	r.handle(context.Background(), InboundMessage{Platform: "rec", ChatID: "1", Text: "hi"})

	final := finalReply(rec, "the answer")
	if final == nil {
		t.Fatal("no final reply sent")
	}
	if final.Audio != nil {
		t.Fatalf("text inbound must not get audio in in-kind mode, got %+v", final.Audio)
	}
}

func TestRunner_SessionPrefOutranksGlobalMode(t *testing.T) {
	prefs := NewVoicePrefs("")

	// Pref "always" speaks a text message even in default in-kind mode.
	rec := &recordingAdapter{}
	r := newVoiceRunner(t, rec, "the answer")
	setEchoSynthesizer(r)
	r.SetVoicePrefs(prefs)
	if err := prefs.Set("rec:1", VoicePrefAlways); err != nil {
		t.Fatal(err)
	}
	r.handle(context.Background(), InboundMessage{Platform: "rec", ChatID: "1", Text: "hi"})
	if final := finalReply(rec, "the answer"); final == nil || final.Audio == nil {
		t.Fatal("session pref always must attach audio to a text message")
	}

	// Pref "never" silences even a voice message under global always mode.
	rec2 := &recordingAdapter{}
	r2 := newVoiceRunner(t, rec2, "the answer")
	setEchoSynthesizer(r2)
	r2.SetVoiceMode(VoiceModeAlways)
	r2.SetVoicePrefs(prefs)
	if err := prefs.Set("rec:2", VoicePrefNever); err != nil {
		t.Fatal(err)
	}
	r2.handle(context.Background(), InboundMessage{
		Platform: "rec", ChatID: "2", Text: "hi",
		Audio: &InboundAudio{Data: []byte("note"), MimeType: "audio/ogg"},
	})
	if final := finalReply(rec2, "the answer"); final == nil || final.Audio != nil {
		t.Fatal("session pref never must keep the reply text-only")
	}
}

func TestRunner_AlwaysModeSpeaksTextMessages(t *testing.T) {
	rec := &recordingAdapter{}
	r := newVoiceRunner(t, rec, "the answer")
	setEchoSynthesizer(r)
	r.SetVoiceMode(VoiceModeAlways)

	r.handle(context.Background(), InboundMessage{Platform: "rec", ChatID: "1", Text: "hi"})

	final := finalReply(rec, "the answer")
	if final == nil {
		t.Fatal("no final reply sent")
	}
	if final.Audio == nil || string(final.Audio.Data) != "AUDIO:the answer" {
		t.Fatalf("expected audio attached in always mode, got %+v", final.Audio)
	}
}

func TestRunner_NoVoiceByDefault(t *testing.T) {
	rec := &recordingAdapter{}
	r := newVoiceRunner(t, rec, "plain reply")
	r.handle(context.Background(), InboundMessage{Platform: "rec", ChatID: "1", Text: "hi"})

	for _, m := range rec.finals() {
		if m.Audio != nil {
			t.Fatalf("no synthesizer set, but audio attached: %+v", m.Audio)
		}
	}
}
