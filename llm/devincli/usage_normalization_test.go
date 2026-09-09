/*
 * ChatCLI - Devin trajectory usage schema contract
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * The Devin CLI fronts several backends and copies each one's counters into
 * the same ATIF fields, so the schema follows the model that generated —
 * which the trajectory names.
 */
package devincli

import "testing"

func TestTrajectoryUsage_AnthropicBackendIsAdditive(t *testing.T) {
	raw := []byte(`{
	  "agent": {"model_name": "claude-sonnet-4-6"},
	  "steps": [
	    {"source": "user", "metadata": {"is_user_input": true}},
	    {"source": "agent", "metadata": {"generation_model": "claude-sonnet-4-6",
	      "metrics": {"input_tokens": 1000, "output_tokens": 200,
	                  "cache_creation_tokens": 500, "cache_read_tokens": 11000}}}
	  ]}`)
	got, err := parseTrajectoryUsage(raw)
	if err != nil || got.Usage == nil {
		t.Fatalf("parse: %v %+v", err, got.Usage)
	}
	if got.Usage.InputTokensTotal != 12500 {
		t.Fatalf("InputTokensTotal = %d, want 12500", got.Usage.InputTokensTotal)
	}
}

func TestTrajectoryUsage_OpenAIBackendIsSubset(t *testing.T) {
	// Same field names, OpenAI backend: cache_read_tokens is the cached
	// SHARE of input_tokens — adding it would invent 10K tokens per turn.
	raw := []byte(`{
	  "agent": {"model_name": "gpt-6-astra"},
	  "steps": [
	    {"source": "agent", "metadata": {"generation_model": "gpt-6-astra",
	      "metrics": {"input_tokens": 22858, "output_tokens": 28,
	                  "cache_read_tokens": 10515}}}
	  ]}`)
	got, err := parseTrajectoryUsage(raw)
	if err != nil || got.Usage == nil {
		t.Fatalf("parse: %v %+v", err, got.Usage)
	}
	if got.Usage.InputTokensTotal != 22858 {
		t.Fatalf("InputTokensTotal = %d, want 22858", got.Usage.InputTokensTotal)
	}
}

// TestTrajectoryUsage_ATIFStandardBlockIsSubset: prompt_tokens/cached_tokens
// is OpenAI-shaped by definition, whatever model produced it.
func TestTrajectoryUsage_ATIFStandardBlockIsSubset(t *testing.T) {
	raw := []byte(`{
	  "agent": {"model_name": "claude-sonnet-4-6"},
	  "steps": [
	    {"source": "agent", "metrics": {"prompt_tokens": 5000, "completion_tokens": 100,
	                                     "cached_tokens": 4000, "cost_usd": 0.01}}
	  ]}`)
	got, err := parseTrajectoryUsage(raw)
	if err != nil || got.Usage == nil {
		t.Fatalf("parse: %v %+v", err, got.Usage)
	}
	if got.Usage.InputTokensTotal != 5000 {
		t.Fatalf("InputTokensTotal = %d, want 5000", got.Usage.InputTokensTotal)
	}
}

func TestDevinCacheAccountingClassification(t *testing.T) {
	cases := []struct {
		native   bool
		model    string
		additive bool
	}{
		{true, "claude-sonnet-4-6", true},
		{true, "claude-fable-5-1", true},
		{true, "anthropic/claude-opus-5", true},
		{true, "gpt-6-astra", false},
		{true, "kimi-k3", false},
		{false, "claude-sonnet-4-6", false}, // ATIF standard block
		{true, "", false},                   // unknown backend: never inflate
	}
	for _, c := range cases {
		got := devinCacheAccounting(c.native, c.model)
		want := "subset"
		if c.additive {
			want = "additive"
		}
		gotName := "subset"
		if got == 1 { // models.CacheAdditive
			gotName = "additive"
		}
		if gotName != want {
			t.Errorf("devinCacheAccounting(%v, %q) = %s, want %s", c.native, c.model, gotName, want)
		}
	}
}
