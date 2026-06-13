/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
/*
 * ffmpeg capability surface: the embedded engine decodes WAV natively and
 * OGG/Opus through the pure-Go decoder, so ffmpeg is only needed for the
 * residual compressed formats (mp3, m4a/aac, flac, …). This file centralizes
 * the sentinel error callers match on, the per-platform install hint, and the
 * startup preflight the gateway uses to tell the operator — before the first
 * voice note fails — exactly what works and what is missing.
 */
package transcription

import (
	"errors"
	"runtime"
)

// ErrNeedsFFmpeg reports an audio payload that none of the built-in decoders
// handle on this machine. Callers match it with errors.Is to answer with an
// actionable, platform-aware instruction instead of a generic failure.
var ErrNeedsFFmpeg = errors.New("ffmpeg is required to decode this audio format")

// residualFFmpegFormats are the compressed formats that still require ffmpeg
// once WAV (native) and OGG/Opus (pure Go) are covered.
var residualFFmpegFormats = []string{"mp3", "m4a/aac", "flac", "wma"}

// FFmpegInstallHint returns the ffmpeg install command for the running
// platform, for embedding in errors, logs and user-facing replies.
func FFmpegInstallHint() string { return installHintFor(runtime.GOOS) }

// installHintFor is the GOOS-parameterized core, separated for tests.
func installHintFor(goos string) string {
	switch goos {
	case "darwin":
		return "brew install ffmpeg"
	case "windows":
		return "winget install --id Gyan.FFmpeg"
	default:
		return "sudo apt-get install ffmpeg (Debian/Ubuntu) · sudo dnf install ffmpeg (Fedora/RHEL) · apk add ffmpeg (Alpine)"
	}
}

// VoicePreflight is the result of inspecting a provider's audio-decoding
// capabilities in the current environment.
type VoicePreflight struct {
	// Provider is the provider's display name.
	Provider string
	// FFmpegPresent reports whether ffmpeg was found on PATH.
	FFmpegPresent bool
	// PureGoOggOpus reports whether OGG/Opus voice notes (Telegram, WhatsApp)
	// decode without ffmpeg on this provider.
	PureGoOggOpus bool
	// NeedsFFmpegFormats lists formats that cannot be decoded in the current
	// environment; empty means full coverage.
	NeedsFFmpegFormats []string
}

// voicePreflighter is the optional capability interface a provider implements
// to describe its decoding requirements. Fail-closed like the other
// capability interfaces: providers without it report no degradation because
// they do their own decoding (cloud APIs, user-supplied commands).
type voicePreflighter interface {
	voicePreflight() VoicePreflight
}

// PreflightVoice inspects the provider's audio-decoding capabilities so the
// gateway can warn the operator at startup — and surface in /gateway status —
// instead of failing on the first voice note.
func PreflightVoice(p Provider) VoicePreflight {
	if p == nil {
		return VoicePreflight{}
	}
	if v, ok := p.(voicePreflighter); ok {
		return v.voicePreflight()
	}
	return VoicePreflight{Provider: p.Name(), FFmpegPresent: lookupFFmpeg() != ""}
}

// voicePreflight reports the embedded engine's decode coverage: WAV is
// native, OGG/Opus is pure Go, everything else needs ffmpeg.
func (e *embeddedWhisper) voicePreflight() VoicePreflight {
	pf := VoicePreflight{
		Provider:      e.Name(),
		FFmpegPresent: lookupFFmpeg() != "",
		PureGoOggOpus: true,
	}
	if !pf.FFmpegPresent {
		pf.NeedsFFmpegFormats = append([]string(nil), residualFFmpegFormats...)
	}
	return pf
}
