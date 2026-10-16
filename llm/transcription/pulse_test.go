/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package transcription

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/diillson/chatcli/pkg/pulse"
	"github.com/diillson/chatcli/pkg/pulse/pulsetest"
)

// pulseFakeSTT returns a fixed transcript.
type pulseFakeSTT struct{ err error }

func (*pulseFakeSTT) Name() string { return "openai:whisper-1" }
func (f *pulseFakeSTT) Transcribe(context.Context, []byte, string, string, string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return "SECRET-TRANSCRIPT", nil
}

func TestTranscribeTracedShowsOnDashboard(t *testing.T) {
	rec := pulsetest.Record(t)
	ctx := pulse.WithParent(context.Background(), "rpc-9")
	audio := make([]byte, 2048)
	if _, err := TranscribeTraced(ctx, &pulseFakeSTT{}, audio, "audio/ogg; codecs=opus", "SECRET-name.ogg", "pt"); err != nil {
		t.Fatal(err)
	}
	ev := rec.WaitFor(t, "transcribe end", pulsetest.Is(pulse.KindTool, pulseTranscribe, pulse.PhaseEnd))
	want := map[string]string{
		"provider": "openai", "engine": "remote", "bytes_in": "2048", "format": "ogg", "chars_out": "17",
	}
	for k, v := range want {
		if ev.Attrs[k] != v {
			t.Errorf("attr %s = %q, want %q (all %v)", k, ev.Attrs[k], v, ev.Attrs)
		}
	}
	if ev.Parent != "rpc-9" || ev.Status != pulse.StatusOK {
		t.Errorf("parent %q status %q", ev.Parent, ev.Status)
	}
	for _, e := range rec.Events() {
		for _, v := range e.Attrs {
			if strings.Contains(v, "SECRET") {
				t.Fatalf("leak onto the bus: %q", v)
			}
		}
	}
}

func TestTranscribeTracedFailureAndModels(t *testing.T) {
	rec := pulsetest.Record(t)
	if _, err := TranscribeTraced(context.Background(), &pulseFakeSTT{err: errors.New("x")}, nil, "", "", ""); err == nil {
		t.Fatal("expected the fake's error")
	}
	ev := rec.WaitFor(t, "failed transcribe", pulsetest.Is(pulse.KindTool, pulseTranscribe, pulse.PhaseEnd))
	if ev.Status != pulse.StatusError {
		t.Errorf("status %q, want error", ev.Status)
	}
	if got := transcribeModel(&OpenAICompatible{model: "whisper-1"}); got != "whisper-1" {
		t.Errorf("model %q", got)
	}
	if got := transcribeModel(&embeddedWhisper{size: "base.en"}); got != "whisper-base.en" {
		t.Errorf("embedded model %q", got)
	}
	if got := audioFormat("nonsense"); got != "" {
		t.Errorf("format %q", got)
	}
}
