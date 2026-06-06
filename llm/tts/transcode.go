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
	"os/exec"
	"strings"

	"go.uber.org/zap"
)

// hasFFmpegTTS reports whether ffmpeg is resolvable on PATH. Indirected so
// tests can force the "not installed" branch; the affirmative branch resolves
// the literal name through exec at run time (tests inject a fake via PATH).
var hasFFmpegTTS = func() bool {
	_, err := exec.LookPath("ffmpeg")
	return err == nil
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
	if !hasFFmpegTTS() {
		logger.Warn("tts: ffmpeg not found; sending raw " + a.Ext + " audio that messengers may not play")
		return a
	}
	ogg, err := toOpus(ctx, a.Data, a.Ext)
	if err != nil {
		logger.Warn("tts: voice-note transcode failed; sending original audio", zap.Error(err))
		return a
	}
	mime, ext := mimeFor("ogg")
	return Audio{Data: ogg, Mime: mime, Ext: ext}
}

// wavToOpus transcodes a WAV clip to OGG/Opus tuned for speech: 48kHz mono at
// 32kbps, the profile Telegram voice notes expect.
func wavToOpus(ctx context.Context, wav []byte) ([]byte, error) {
	return toOpus(ctx, wav, "wav")
}

// toOpus transcodes a raw clip to OGG/Opus by streaming through ffmpeg: audio
// in on stdin, ogg out on stdout. No temp files and a fully literal argv —
// the binary name resolves through PATH inside the exec package and ffmpeg
// probes the input container (wav/aiff) from the stream itself, so there is
// nothing variable in the command at all. srcExt is kept for error context.
func toOpus(ctx context.Context, audio []byte, srcExt string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "ffmpeg", "-hide_banner", "-loglevel", "error",
		"-i", "pipe:0", "-c:a", "libopus", "-b:a", "32k", "-ar", "48000", "-ac", "1",
		"-f", "ogg", "pipe:1")
	var out, stderr bytes.Buffer
	cmd.Stdin = bytes.NewReader(audio)
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return nil, fmt.Errorf("tts: ffmpeg transcode from %s failed: %w: %s", srcExt, err, lastLine(msg))
		}
		return nil, fmt.Errorf("tts: ffmpeg transcode from %s failed: %w", srcExt, err)
	}
	if out.Len() == 0 {
		return nil, fmt.Errorf("tts: ffmpeg produced no audio")
	}
	return out.Bytes(), nil
}

// lastLine trims ffmpeg's chatty stderr to its final line for error messages.
func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}
