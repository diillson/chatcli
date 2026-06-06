/*
 * ChatCLI - WAV to OGG/Opus transcoding for voice notes.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * The embedded engine produces 24kHz WAV. Telegram only renders a clip as a
 * native voice note when it arrives as OGG/Opus, so when the caller asks for
 * ogg/opus and ffmpeg is installed we transcode; otherwise the WAV is sent as
 * a regular audio file — degraded presentation, never a lost reply.
 */
package tts

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"go.uber.org/zap"
)

// lookupFFmpegTTS returns the ffmpeg binary path, or "" when not installed.
// Indirected so tests can exercise the transcode path without a real ffmpeg.
var lookupFFmpegTTS = func() string {
	p, _ := exec.LookPath("ffmpeg")
	return p
}

// ToVoiceNote converts a synthesized clip into OGG/Opus when the backend
// produced a raw format messengers cannot play inline — the local macOS `say`
// engine emits aiff and espeak emits wav, both of which Telegram renders as a
// dead file. Compressed formats pass through untouched, and without ffmpeg
// the clip is returned unchanged so the reply still goes out.
func ToVoiceNote(ctx context.Context, a Audio, logger *zap.Logger) Audio {
	switch a.Ext {
	case "wav", "aiff":
		// raw PCM containers: worth transcoding
	default:
		return a
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	ffmpeg := lookupFFmpegTTS()
	if ffmpeg == "" {
		logger.Warn("tts: ffmpeg not found; sending raw " + a.Ext + " audio that messengers may not play")
		return a
	}
	ogg, err := toOpus(ctx, ffmpeg, a.Data, a.Ext)
	if err != nil {
		logger.Warn("tts: voice-note transcode failed; sending original audio", zap.Error(err))
		return a
	}
	mime, ext := mimeFor("ogg")
	return Audio{Data: ogg, Mime: mime, Ext: ext}
}

// wavToOpus transcodes a WAV clip to OGG/Opus tuned for speech: 48kHz mono at
// 32kbps, the profile Telegram voice notes expect.
func wavToOpus(ctx context.Context, ffmpeg string, wav []byte) ([]byte, error) {
	return toOpus(ctx, ffmpeg, wav, "wav")
}

// toOpus runs the actual ffmpeg transcode from srcExt (wav/aiff — ffmpeg
// detects the container by content, the extension is a hint) to OGG/Opus.
func toOpus(ctx context.Context, ffmpeg string, audio []byte, srcExt string) ([]byte, error) {
	in, err := os.CreateTemp("", "chatcli-tts-in-*."+srcExt)
	if err != nil {
		return nil, fmt.Errorf("tts: temp input: %w", err)
	}
	inPath := in.Name()
	defer func() { _ = os.Remove(inPath) }()
	if _, err := in.Write(audio); err != nil {
		_ = in.Close()
		return nil, fmt.Errorf("tts: write temp input: %w", err)
	}
	if err := in.Close(); err != nil {
		return nil, fmt.Errorf("tts: close temp input: %w", err)
	}

	outPath := inPath + ".ogg"
	defer func() { _ = os.Remove(outPath) }()

	cmd := exec.CommandContext(ctx, ffmpeg, "-nostdin", "-y", "-i", inPath,
		"-c:a", "libopus", "-b:a", "32k", "-ar", "48000", "-ac", "1", "-f", "ogg", outPath)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return nil, fmt.Errorf("tts: ffmpeg transcode failed: %w: %s", err, lastLine(msg))
		}
		return nil, fmt.Errorf("tts: ffmpeg transcode failed: %w", err)
	}
	data, err := os.ReadFile(outPath) // #nosec G304 -- temp file we created
	if err != nil {
		return nil, fmt.Errorf("tts: read transcoded output: %w", err)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("tts: ffmpeg produced no audio")
	}
	return data, nil
}

// lastLine trims ffmpeg's chatty stderr to its final line for error messages.
func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}
