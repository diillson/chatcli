/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"strconv"
	"testing"
)

// The /max-tokens presets must follow the active model's real output
// ceiling: a 128K model (Opus 4.8 on Bedrock) surfaces the full ladder up
// to its cap, while a smaller model never gets suggestions above a value
// its provider would hard-reject.
func TestMaxTokensPresetsFollowModelCeiling(t *testing.T) {
	cli := &ChatCLI{Provider: "BEDROCK", Model: "global.anthropic.claude-opus-4-8"}
	presets := cli.maxTokensPresets()

	values := make(map[int]bool)
	for _, s := range presets {
		n, err := strconv.Atoi(s.Text)
		if err != nil {
			t.Fatalf("non-numeric preset %q", s.Text)
		}
		values[n] = true
	}
	for _, want := range []int{0, 1024, 4096, 8192, 16384, 32768, 65536, 128000} {
		if !values[want] {
			t.Errorf("128K model must offer preset %d, got %v", want, values)
		}
	}
	if values[131072] {
		t.Errorf("ladder must stop at the 128000 ceiling, got %v", values)
	}
}

func TestMaxTokensPresetsSmallModel(t *testing.T) {
	// Nova Micro caps at 5120 — nothing above it may be suggested, and the
	// exact ceiling is offered.
	cli := &ChatCLI{Provider: "BEDROCK", Model: "us.amazon.nova-micro-v1:0"}
	max := 0
	sawCeiling := false
	for _, s := range cli.maxTokensPresets() {
		n, err := strconv.Atoi(s.Text)
		if err != nil {
			t.Fatalf("non-numeric preset %q", s.Text)
		}
		if n > max {
			max = n
		}
		if n == 5120 {
			sawCeiling = true
		}
	}
	if max > 5120 {
		t.Errorf("suggested %d above the 5120 model ceiling", max)
	}
	if !sawCeiling {
		t.Errorf("the model ceiling itself must be offered")
	}
}

func TestHumanTokenCount(t *testing.T) {
	cases := map[int]string{
		1024:   "1K",
		4096:   "4K",
		65536:  "64K",
		131072: "128K",
		64000:  "64K",
		128000: "128K",
		5120:   "5K",
		12345:  "12345",
	}
	for n, want := range cases {
		if got := humanTokenCount(n); got != want {
			t.Errorf("humanTokenCount(%d) = %q, want %q", n, got, want)
		}
	}
}
