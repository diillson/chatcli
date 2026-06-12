/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package transcription

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/zap"
)

// stubFFmpeg replaces the ffmpeg lookup for the test's duration.
func stubFFmpeg(t *testing.T, path string) {
	t.Helper()
	orig := lookupFFmpeg
	lookupFFmpeg = func() string { return path }
	t.Cleanup(func() { lookupFFmpeg = orig })
}

func TestFFmpegInstallHintPerPlatform(t *testing.T) {
	tests := map[string]string{
		"darwin":  "brew install ffmpeg",
		"windows": "winget install --id Gyan.FFmpeg",
		"linux":   "apt-get install ffmpeg",
		"freebsd": "apt-get install ffmpeg", // generic unix hint
	}
	for goos, want := range tests {
		if got := installHintFor(goos); !strings.Contains(got, want) {
			t.Errorf("installHintFor(%q) = %q, want it to mention %q", goos, got, want)
		}
	}
}

func TestEmbeddedToWAVPureGoOpusWithoutFFmpeg(t *testing.T) {
	stubFFmpeg(t, "")
	clip, err := os.ReadFile(filepath.Join("testdata", "voice_voip.ogg"))
	if err != nil {
		t.Fatal(err)
	}

	e := NewEmbeddedWhisper("", zap.NewNop())
	wav, err := e.toWAV(context.Background(), clip, "audio/ogg", "voice.ogg")
	if err != nil {
		t.Fatalf("toWAV should decode OGG/Opus in pure Go: %v", err)
	}
	if len(wav) < 44 || string(wav[:4]) != "RIFF" {
		t.Fatalf("not a WAV payload (len=%d)", len(wav))
	}
	if rate := binary.LittleEndian.Uint32(wav[24:28]); rate != engineSampleRate {
		t.Errorf("sample rate = %d, want %d", rate, engineSampleRate)
	}
}

func TestEmbeddedToWAVPassesThroughWAV(t *testing.T) {
	stubFFmpeg(t, "")
	wavIn := append([]byte("RIFF\x00\x00\x00\x00WAVE"), make([]byte, 64)...)

	e := NewEmbeddedWhisper("", zap.NewNop())
	out, err := e.toWAV(context.Background(), wavIn, "audio/wav", "clip.wav")
	if err != nil {
		t.Fatalf("WAV must pass through without ffmpeg: %v", err)
	}
	if &out[0] != &wavIn[0] {
		t.Error("WAV passthrough should not copy the payload")
	}
}

func TestEmbeddedToWAVUnsupportedFormatYieldsSentinel(t *testing.T) {
	stubFFmpeg(t, "")
	mp3ish := append([]byte("ID3\x04\x00"), make([]byte, 128)...)

	e := NewEmbeddedWhisper("", zap.NewNop())
	_, err := e.toWAV(context.Background(), mp3ish, "audio/mpeg", "clip.mp3")
	if !errors.Is(err, ErrNeedsFFmpeg) {
		t.Fatalf("err = %v, want ErrNeedsFFmpeg", err)
	}
	if !strings.Contains(err.Error(), "audio/mpeg") {
		t.Errorf("error should name the rejected format: %v", err)
	}
	if !strings.Contains(err.Error(), FFmpegInstallHint()) {
		t.Errorf("error should carry the platform install hint: %v", err)
	}
}

func TestPreflightVoiceEmbedded(t *testing.T) {
	e := NewEmbeddedWhisper("", zap.NewNop())

	stubFFmpeg(t, "")
	pf := PreflightVoice(e)
	if !pf.PureGoOggOpus {
		t.Error("embedded must report pure-Go OGG/Opus support")
	}
	if pf.FFmpegPresent || len(pf.NeedsFFmpegFormats) == 0 {
		t.Errorf("without ffmpeg the residual formats must be reported: %+v", pf)
	}

	stubFFmpeg(t, "/usr/bin/ffmpeg")
	pf = PreflightVoice(e)
	if !pf.FFmpegPresent || len(pf.NeedsFFmpegFormats) != 0 {
		t.Errorf("with ffmpeg there is no degradation to report: %+v", pf)
	}
}

func TestPreflightVoiceNonEmbeddedProviders(t *testing.T) {
	stubFFmpeg(t, "")
	pf := PreflightVoice(NewNull())
	if len(pf.NeedsFFmpegFormats) != 0 {
		t.Errorf("providers that own their decoding must report no degradation: %+v", pf)
	}
	if pf := PreflightVoice(nil); pf.Provider != "" {
		t.Errorf("nil provider must yield a zero preflight: %+v", pf)
	}
}
