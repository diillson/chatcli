/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package catalog

import "testing"

func TestGetCompactRatio_DefaultsToZeroAndHonorsEntry(t *testing.T) {
	if r := GetCompactRatio(ProviderClaudeAI, "claude-sonnet-5"); r != 0 {
		t.Fatalf("entries without a ratio return 0, got %v", r)
	}
	if r := GetCompactRatio("NOPE", "unknown-model"); r != 0 {
		t.Fatalf("unknown models return 0, got %v", r)
	}
	Register(ModelMeta{ID: "ratio-test-model", Aliases: []string{"ratio-test-model"}, Provider: "TESTPROV", ContextWindow: 100000, CompactRatio: 0.55})
	if r := GetCompactRatio("TESTPROV", "ratio-test-model"); r != 0.55 {
		t.Fatalf("declared ratio must resolve, got %v", r)
	}
	Register(ModelMeta{ID: "ratio-bad-model", Aliases: []string{"ratio-bad-model"}, Provider: "TESTPROV", ContextWindow: 100000, CompactRatio: 1.5})
	if r := GetCompactRatio("TESTPROV", "ratio-bad-model"); r != 0 {
		t.Fatalf("ratios above 1 are ignored, got %v", r)
	}
}
