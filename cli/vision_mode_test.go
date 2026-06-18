/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"testing"

	"go.uber.org/zap"
)

func TestModelIDImpliesVision(t *testing.T) {
	yes := []string{
		"qwen2.5-vl-72b", "Pixtral-12B", "llava-1.6", "internvl2-8b",
		"some-vision-preview", "minimax-vl-01", "gpt-4o-omni",
	}
	no := []string{
		"gpt-4-turbo-instruct", "deepseek-r1", "mistral-large",
		"o3-mini", "claude-3-5-haiku", "text-embedding-3-large",
	}
	for _, m := range yes {
		if !modelIDImpliesVision(m) {
			t.Errorf("%q should be detected as a vision model id", m)
		}
	}
	for _, m := range no {
		if modelIDImpliesVision(m) {
			t.Errorf("%q must NOT be falsely detected as vision (would hard-error the API)", m)
		}
	}
}

func TestResolveVisionMode_Override(t *testing.T) {
	cli := &ChatCLI{logger: zap.NewNop(), Provider: "SOMEPROVIDER", Model: "some-text-model"}

	t.Setenv("CHATCLI_VISION_INPUT", "native")
	if cli.resolveVisionMode() != visionNative {
		t.Error("native override must force native")
	}
	t.Setenv("CHATCLI_VISION_INPUT", "off")
	if cli.resolveVisionMode() != visionOff {
		t.Error("off override must disable images")
	}
	t.Setenv("CHATCLI_VISION_INPUT", "describe")
	if cli.resolveVisionMode() != visionDescribe {
		t.Error("describe override must force describe")
	}
}

func TestResolveVisionMode_HeuristicForOffCatalog(t *testing.T) {
	t.Setenv("CHATCLI_VISION_INPUT", "") // auto
	// An off-catalog model whose id implies vision resolves to native.
	cli := &ChatCLI{logger: zap.NewNop(), Provider: "SOMEPROVIDER", Model: "qwen2.5-vl-7b-instruct"}
	if cli.resolveVisionMode() != visionNative {
		t.Error("off-catalog vision id should resolve to native via heuristic")
	}
	// An off-catalog text model falls back to describe (never native).
	cli.Model = "some-unknown-text-model"
	if cli.resolveVisionMode() != visionDescribe {
		t.Error("off-catalog non-vision id must fall back to describe, not native")
	}
}
