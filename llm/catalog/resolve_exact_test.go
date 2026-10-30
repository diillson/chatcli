/*
 * ChatCLI - Tests for exact catalog resolution and text-only providers
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package catalog

import "testing"

// A modality variant resolves loosely to its base model; exact resolution
// must not hand it the base model's capabilities.
func TestResolveExact_NoLooseMatch(t *testing.T) {
	if _, ok := Resolve(ProviderGoogleAI, "gemini-2.5-pro-preview-tts"); !ok {
		t.Fatal("fixture: the loose resolver matches the TTS variant")
	}
	if _, ok := ResolveExact(ProviderGoogleAI, "gemini-2.5-pro-preview-tts"); ok {
		t.Fatal("exact resolution matched a modality variant")
	}
	if meta, ok := ResolveExact(ProviderGoogleAI, "GEMINI-3.8-FLASH-LATEST"); !ok || meta.ID != "gemini-3.8-flash" {
		t.Fatalf("an exact alias must resolve, case-insensitively: %+v %v", meta, ok)
	}
	if _, ok := ResolveExact(ProviderClaudeAI, "gemini-3.8-flash"); ok {
		t.Fatal("exact resolution crossed providers")
	}
}

func TestProviderCarriesImages(t *testing.T) {
	for _, p := range []string{ProviderOpenAIAssistant, ProviderStackSpot, ProviderDevin} {
		if ProviderCarriesImages(p) {
			t.Errorf("%s sends text only", p)
		}
	}
	for _, p := range []string{ProviderOpenAI, ProviderClaudeAI, ProviderBedrock, ProviderGoogleAI, ProviderXAI, ProviderZAI,
		ProviderMoonshot, ProviderMiniMax, ProviderOllama, ProviderCopilot, ProviderOpenRouter, "SOME_PLUGIN"} {
		if !ProviderCarriesImages(p) {
			t.Errorf("%s carries images", p)
		}
	}
}
