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

func TestRunner_VoiceAttachedToFinal(t *testing.T) {
	rec := &recordingAdapter{}
	r := newVoiceRunner(t, rec, "the answer")
	r.SetVoiceSynthesizer(func(_ context.Context, text string) *OutboundAudio {
		return &OutboundAudio{Data: []byte("AUDIO:" + text), Mime: "audio/ogg", FileName: "reply.ogg"}
	})

	r.handle(context.Background(), InboundMessage{Platform: "rec", ChatID: "1", Text: "hi"})

	var final *OutboundMessage
	for i := range rec.finals() {
		m := rec.finals()[i]
		if m.Text == "the answer" {
			final = &m
		}
	}
	if final == nil {
		t.Fatal("no final reply sent")
	}
	if final.Audio == nil || string(final.Audio.Data) != "AUDIO:the answer" {
		t.Fatalf("expected audio attached, got %+v", final.Audio)
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
