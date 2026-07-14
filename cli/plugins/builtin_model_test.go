/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package plugins

import (
	"context"
	"strings"
	"testing"
)

// fakeModelRoutingAdapter records calls and returns canned results.
type fakeModelRoutingAdapter struct {
	lastCmd    string
	lastHandle string
	lastPrompt string
	lastMax    int
}

func (f *fakeModelRoutingAdapter) List(_ context.Context, providerFilter string) (string, error) {
	f.lastCmd, f.lastHandle = "list", providerFilter
	return "providers", nil
}

func (f *fakeModelRoutingAdapter) Use(_ context.Context, handle string) (string, error) {
	f.lastCmd, f.lastHandle = "use", handle
	return "switched", nil
}

func (f *fakeModelRoutingAdapter) Reset() (string, error) {
	f.lastCmd = "reset"
	return "reset", nil
}

func (f *fakeModelRoutingAdapter) Status() (string, error) {
	f.lastCmd = "status"
	return "status", nil
}

func (f *fakeModelRoutingAdapter) Delegate(_ context.Context, handle, prompt string, maxTokens int) (string, error) {
	f.lastCmd, f.lastHandle, f.lastPrompt, f.lastMax = "delegate", handle, prompt, maxTokens
	return "delegated", nil
}

func withFakeModelAdapter(t *testing.T) *fakeModelRoutingAdapter {
	t.Helper()
	f := &fakeModelRoutingAdapter{}
	SetModelRoutingAdapter(f)
	t.Cleanup(func() { SetModelRoutingAdapter(nil) })
	return f
}

func TestParseModelInvocation(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantCmd    string
		wantModel  string
		wantPrompt string
		wantMax    int
	}{
		{"empty defaults to status", nil, "status", "", "", 0},
		{"envelope use", []string{`{"cmd":"use","args":{"model":"CLAUDEAI:claude-haiku-4-5"}}`}, "use", "CLAUDEAI:claude-haiku-4-5", "", 0},
		{"bare json implies use", []string{`{"model":"haiku"}`}, "use", "haiku", "", 0},
		{"envelope delegate with max_tokens", []string{`{"cmd":"delegate","args":{"model":"GOOGLEAI:gemini-2.5-flash","prompt":"summarize this","max_tokens":512}}`}, "delegate", "GOOGLEAI:gemini-2.5-flash", "summarize this", 512},
		{"positional use", []string{"use", "haiku"}, "use", "haiku", "", 0},
		{"positional list", []string{"list"}, "list", "", "", 0},
		{"bare model implies use", []string{"haiku"}, "use", "haiku", "", 0},
		{"flattener flag form", []string{"--cmd", "use", "--model", "CLAUDEAI:claude-haiku-4-5"}, "use", "CLAUDEAI:claude-haiku-4-5", "", 0},
		{"flag eq form", []string{"use", "--model=haiku"}, "use", "haiku", "", 0},
		{"positional delegate joins prompt", []string{"delegate", "GOOGLEAI:gemini-2.5-flash", "summarize", "the", "log"}, "delegate", "GOOGLEAI:gemini-2.5-flash", "summarize the log", 0},
		{"synonyms", []string{"switch", "haiku"}, "use", "haiku", "", 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			inv, err := parseModelInvocation(tc.args)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if inv.cmd != tc.wantCmd || inv.model != tc.wantModel || inv.prompt != tc.wantPrompt || inv.maxTokens != tc.wantMax {
				t.Errorf("got {cmd:%q model:%q prompt:%q max:%d}, want {cmd:%q model:%q prompt:%q max:%d}",
					inv.cmd, inv.model, inv.prompt, inv.maxTokens, tc.wantCmd, tc.wantModel, tc.wantPrompt, tc.wantMax)
			}
		})
	}
}

func TestBuiltinModelDispatch(t *testing.T) {
	p := NewBuiltinModelPlugin()

	t.Run("no adapter", func(t *testing.T) {
		SetModelRoutingAdapter(nil)
		if _, err := p.Execute(context.Background(), []string{"list"}); err == nil {
			t.Fatal("expected error without adapter")
		}
	})

	t.Run("use requires model", func(t *testing.T) {
		withFakeModelAdapter(t)
		if _, err := p.Execute(context.Background(), []string{`{"cmd":"use"}`}); err == nil {
			t.Fatal("expected error for use without model")
		}
	})

	t.Run("delegate requires prompt", func(t *testing.T) {
		withFakeModelAdapter(t)
		if _, err := p.Execute(context.Background(), []string{`{"cmd":"delegate","args":{"model":"x"}}`}); err == nil {
			t.Fatal("expected error for delegate without prompt")
		}
	})

	t.Run("routes to adapter", func(t *testing.T) {
		f := withFakeModelAdapter(t)
		out, err := p.Execute(context.Background(), []string{`{"cmd":"delegate","args":{"model":"GOOGLEAI:gemini-2.5-flash","prompt":"do it","max_tokens":128}}`})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if out != "delegated" || f.lastHandle != "GOOGLEAI:gemini-2.5-flash" || f.lastPrompt != "do it" || f.lastMax != 128 {
			t.Errorf("adapter saw {handle:%q prompt:%q max:%d}, out=%q", f.lastHandle, f.lastPrompt, f.lastMax, out)
		}
	})
}

func TestBuiltinModelCapabilities(t *testing.T) {
	p := NewBuiltinModelPlugin()

	if !p.IsReadOnly([]string{`{"cmd":"list"}`}) || !p.IsReadOnly([]string{`{"cmd":"status"}`}) {
		t.Error("list/status must be read-only")
	}
	if p.IsReadOnly([]string{`{"cmd":"use","args":{"model":"haiku"}}`}) || p.IsReadOnly([]string{"reset"}) {
		t.Error("use/reset must NOT be read-only")
	}
	if p.IsReadOnly([]string{`{"cmd":"delegate","args":{"model":"x","prompt":"y"}}`}) {
		t.Error("delegate must NOT be read-only (spends tokens)")
	}
	if p.IsConcurrencySafe([]string{"use", "haiku"}) {
		t.Error("use must not be concurrency-safe (mutates shared override)")
	}
	if !p.IsConcurrencySafe([]string{`{"cmd":"delegate","args":{"model":"x","prompt":"y"}}`}) {
		t.Error("delegate is an independent one-shot and must be concurrency-safe")
	}
	if !strings.Contains(p.Description(), "PROVIDER:model") {
		t.Error("description must teach the qualified handle form")
	}
}
