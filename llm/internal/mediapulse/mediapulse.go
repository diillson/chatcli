/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

// Package mediapulse holds what the media backends (image generation,
// speech synthesis, transcription) share when they report a call to the
// live dashboard: a provider label that is safe to publish and the engine
// it runs on.
//
// Backend names are built for logs and can carry a local binary's path
// ("command:/usr/local/bin/piper") or a voice; the dashboard groups nodes
// by name and must never see a path, so only the leading provider word
// survives.
package mediapulse

import "strings"

// maxLabel bounds a provider label.
const maxLabel = 32

// ProviderLabel reduces a backend name to its provider word: the part
// before the first ':', '/', '\' or space, lowercased, letters, digits, '-',
// '_' and '.' only. "embedded:kokoro/af_heart" → "embedded"; "" → "unknown".
func ProviderLabel(name string) string {
	head := strings.ToLower(strings.TrimSpace(name))
	if i := strings.IndexAny(head, ":/\\ "); i >= 0 {
		head = head[:i]
	}
	var b strings.Builder
	for _, r := range head {
		if b.Len() >= maxLabel {
			break
		}
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "unknown"
	}
	return b.String()
}

// Engine says where a backend runs: "local" for an engine on this machine
// (the embedded models, a local command), "remote" for anything reached
// over the network.
func Engine(label string) string {
	switch label {
	case "embedded", "local", "command":
		return "local"
	}
	return "remote"
}
