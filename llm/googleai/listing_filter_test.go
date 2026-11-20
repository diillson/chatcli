/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package googleai

import "testing"

// The official listing advertises generateContent on image, speech and
// music models; only the text models belong in the chat listing.
func TestKeepModel_OfficialEndpointDropsNonChatModels(t *testing.T) {
	for id, want := range map[string]bool{
		"gemini-4-neon":                      true,
		"gemini-4-hydrogen":                  true,
		"gemini-4-carbon":                    true,
		"gemini-3.8-flash":                   true,
		"gemini-3.5-flash-lite":              true,
		"gemini-3.1-pro-preview":             true,
		"gemini-flash-latest":                true,
		"gemma-4-31b-it":                     true,
		"deep-research-preview-04-2026":      true,
		"gemini-3.8-flash-tts":               false,
		"gemini-3.8-flash-lite-tts":          false,
		"gemini-2.5-pro-preview-tts":         false,
		"gemini-3.1-flash-image":             false,
		"gemini-3.1-flash-lite-image":        false,
		"gemini-3-pro-image-preview":         false,
		"gemini-nano-banana-2.1":             false,
		"nano-banana-pro-preview":            false,
		"lyria-3.5":                          false,
		"lyria-3-pro-preview":                false,
		"gemini-3.5-transcribe":              false,
		"GEMINI-3.8-FLASH-TTS":               false,
		"imagen-4.0-generate-001":            false,
		"veo-3.1-generate-preview":           false,
		"gemini-2.5-flash-lite":              true,
		"gemini-robotics-er-2-preview":       true,
		"gemini-omni-1.2-flash":              true,
		"antigravity-preview-latest":         true,
		"gemini-3.1-pro-preview-customtools": true,
	} {
		if got := keepModel(id, false); got != want {
			t.Errorf("keepModel(%q, official) = %v, want %v", id, got, want)
		}
		if !keepModel(id, true) {
			t.Errorf("keepModel(%q, custom) must keep everything", id)
		}
	}
}
