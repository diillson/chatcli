/*
 * ChatCLI - TTS abstraction tests.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package tts

import (
	"context"
	"errors"
	"testing"
)

func TestNullProvider(t *testing.T) {
	n := NewNull()
	if n.Name() != "null" {
		t.Fatalf("name = %q", n.Name())
	}
	if !IsNull(n) {
		t.Fatal("IsNull should be true for Null")
	}
	if IsNull(nil) != true {
		t.Fatal("IsNull(nil) should be true")
	}
	_, err := n.Synthesize(context.Background(), "x", "", "")
	if !errors.Is(err, ErrDisabled) {
		t.Fatalf("expected ErrDisabled, got %v", err)
	}
}

func TestMimeFor(t *testing.T) {
	cases := map[string][2]string{
		"mp3":     {"audio/mpeg", "mp3"},
		"":        {"audio/mpeg", "mp3"},
		"wav":     {"audio/wav", "wav"},
		"ogg":     {"audio/ogg", "ogg"},
		"opus":    {"audio/ogg", "ogg"},
		"aac":     {"audio/aac", "aac"},
		"flac":    {"audio/flac", "flac"},
		"aiff":    {"audio/aiff", "aiff"},
		"unknown": {"audio/mpeg", "mp3"},
	}
	for in, want := range cases {
		mime, ext := mimeFor(in)
		if mime != want[0] || ext != want[1] {
			t.Errorf("mimeFor(%q) = (%q,%q), want %v", in, mime, ext, want)
		}
	}
}

func TestOpenAICompatible_EmptyText(t *testing.T) {
	p, _ := NewOpenAICompatible("http://localhost:1", "", "tts-1", "x", nil)
	if _, err := p.Synthesize(context.Background(), "  ", "", ""); err == nil {
		t.Fatal("empty text should error")
	}
}
