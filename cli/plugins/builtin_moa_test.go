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

type fakeMoaAdapter struct {
	lastPrompt string
	lastModels []string
	lastAgg    string
	out        string
}

func (f *fakeMoaAdapter) Run(_ context.Context, prompt string, members []string, aggregator string) (string, error) {
	f.lastPrompt = prompt
	f.lastModels = members
	f.lastAgg = aggregator
	return f.out, nil
}
func (f *fakeMoaAdapter) List(context.Context) (string, error) { return "providers", nil }

func withMoaAdapter(t *testing.T, a MoaAdapter) {
	t.Helper()
	SetMoaAdapter(a)
	t.Cleanup(func() { SetMoaAdapter(nil) })
}

func TestMoa_NoAdapter(t *testing.T) {
	SetMoaAdapter(nil)
	p := NewBuiltinMoaPlugin()
	if _, err := p.Execute(context.Background(), []string{`{"cmd":"ask","args":{"prompt":"x"}}`}); err == nil {
		t.Fatal("expected error with no adapter")
	}
}

func TestMoa_EnvelopeAsk(t *testing.T) {
	f := &fakeMoaAdapter{out: "synth"}
	withMoaAdapter(t, f)
	p := NewBuiltinMoaPlugin()

	out, err := p.Execute(context.Background(), []string{`{"cmd":"ask","args":{"prompt":"why sky blue","models":["openai","anthropic"],"aggregator":"googleai"}}`})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != "synth" {
		t.Fatalf("out = %q", out)
	}
	if f.lastPrompt != "why sky blue" {
		t.Fatalf("prompt = %q", f.lastPrompt)
	}
	if len(f.lastModels) != 2 || f.lastModels[0] != "openai" {
		t.Fatalf("models = %v", f.lastModels)
	}
	if f.lastAgg != "googleai" {
		t.Fatalf("aggregator = %q", f.lastAgg)
	}
}

func TestMoa_ArgvAsk(t *testing.T) {
	f := &fakeMoaAdapter{out: "ok"}
	withMoaAdapter(t, f)
	p := NewBuiltinMoaPlugin()

	if _, err := p.Execute(context.Background(), []string{"ask", "design", "a", "cache"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.lastPrompt != "design a cache" {
		t.Fatalf("prompt = %q", f.lastPrompt)
	}
}

func TestMoa_MissingPrompt(t *testing.T) {
	withMoaAdapter(t, &fakeMoaAdapter{})
	p := NewBuiltinMoaPlugin()
	if _, err := p.Execute(context.Background(), []string{`{"cmd":"ask","args":{}}`}); err == nil {
		t.Fatal("expected error for missing prompt")
	}
}

func TestMoa_List(t *testing.T) {
	withMoaAdapter(t, &fakeMoaAdapter{})
	p := NewBuiltinMoaPlugin()
	out, err := p.Execute(context.Background(), []string{`{"cmd":"list"}`})
	if err != nil || out != "providers" {
		t.Fatalf("list out=%q err=%v", out, err)
	}
}

func TestCanonicalMoaCmd(t *testing.T) {
	for _, in := range []string{"ask", "run", "QUERY", "consult"} {
		if canonicalMoaCmd(in) != "ask" {
			t.Errorf("canonicalMoaCmd(%q) != ask", in)
		}
	}
	for _, in := range []string{"list", "models", "providers"} {
		if canonicalMoaCmd(in) != "list" {
			t.Errorf("canonicalMoaCmd(%q) != list", in)
		}
	}
	if canonicalMoaCmd("zzz") != "" {
		t.Error("unknown cmd should be empty")
	}
}

func TestMoaSchema(t *testing.T) {
	p := NewBuiltinMoaPlugin()
	if !strings.Contains(p.Schema(), "aggregator") || p.Name() != "@moa" {
		t.Fatal("schema/name wrong")
	}
}

func TestMoa_FlattenedArgvFromAgent(t *testing.T) {
	f := &fakeMoaAdapter{out: "ok"}
	withMoaAdapter(t, f)
	p := NewBuiltinMoaPlugin()
	argv := []string{"ask", "--prompt", "why is the sky blue", "--models", "openai", "--models", "anthropic", "--aggregator", "googleai"}
	if _, err := p.Execute(context.Background(), argv); err != nil {
		t.Fatal(err)
	}
	if f.lastPrompt != "why is the sky blue" {
		t.Fatalf("prompt = %q", f.lastPrompt)
	}
	if len(f.lastModels) != 2 || f.lastModels[0] != "openai" || f.lastModels[1] != "anthropic" {
		t.Fatalf("models = %v", f.lastModels)
	}
	if f.lastAgg != "googleai" {
		t.Fatalf("aggregator = %q", f.lastAgg)
	}
}
