/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package tts

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/diillson/chatcli/pkg/pulse"
	"github.com/diillson/chatcli/pkg/pulse/pulsetest"
)

// pulseFakeSynth speaks a fixed clip.
type pulseFakeSynth struct{ err error }

func (*pulseFakeSynth) Name() string { return "command:/opt/secret/bin/piper" }
func (f *pulseFakeSynth) Synthesize(context.Context, string, string, string) (Audio, error) {
	if f.err != nil {
		return Audio{}, f.err
	}
	return Audio{Data: []byte("RIFFxxxx"), Mime: "audio/wav", Ext: "wav"}, nil
}

func TestSynthesizeTracedShowsOnDashboard(t *testing.T) {
	rec := pulsetest.Record(t)
	ctx := pulse.WithParent(context.Background(), "tool-3")
	if _, err := SynthesizeTraced(ctx, &pulseFakeSynth{}, "SECRET-TEXT!", "SECRET-VOICE", "wav"); err != nil {
		t.Fatal(err)
	}
	ev := rec.WaitFor(t, "synthesize end", pulsetest.Is(pulse.KindTool, pulseSynthesize, pulse.PhaseEnd))
	want := map[string]string{
		"provider": "command", "engine": "local", "chars_in": "12", "bytes_out": "8", "format": "wav",
	}
	for k, v := range want {
		if ev.Attrs[k] != v {
			t.Errorf("attr %s = %q, want %q (all %v)", k, ev.Attrs[k], v, ev.Attrs)
		}
	}
	if ev.Parent != "tool-3" || ev.Status != pulse.StatusOK {
		t.Errorf("parent %q status %q", ev.Parent, ev.Status)
	}
	for _, e := range rec.Events() {
		for _, v := range e.Attrs {
			if strings.Contains(v, "SECRET") || strings.Contains(v, "/opt") {
				t.Fatalf("leak onto the bus: %q", v)
			}
		}
	}
}

func TestSynthesizeTracedModelAndFailure(t *testing.T) {
	rec := pulsetest.Record(t)
	if _, err := SynthesizeTraced(context.Background(), &pulseFakeSynth{err: errors.New("boom")}, "x", "", ""); err == nil {
		t.Fatal("expected the fake's error")
	}
	ev := rec.WaitFor(t, "failed synthesize", pulsetest.Is(pulse.KindTool, pulseSynthesize, pulse.PhaseEnd))
	if ev.Status != pulse.StatusError {
		t.Errorf("status %q, want error", ev.Status)
	}
	if got := synthModel(&OpenAICompatible{model: "tts-1"}); got != "tts-1" {
		t.Errorf("model %q", got)
	}
	if got := synthModel(&pulseFakeSynth{}); got != "" {
		t.Errorf("a command has no model, got %q", got)
	}
}
