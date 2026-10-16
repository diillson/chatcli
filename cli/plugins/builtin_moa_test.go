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

// fakeMoaRunner is a MoaRequestRunner that records the request and streams
// one progress line.
type fakeMoaRunner struct {
	fakeMoaAdapter
	req MoaRequest
}

func (f *fakeMoaRunner) RunRequest(_ context.Context, req MoaRequest, onProgress func(string)) (string, error) {
	f.req = req
	if onProgress != nil {
		onProgress("round 1/2")
	}
	return "layered", nil
}

func TestMoa_AskPassesLayersAndPositions(t *testing.T) {
	r := &fakeMoaRunner{}
	SetMoaAdapter(r)
	t.Cleanup(func() { SetMoaAdapter(nil) })
	var streamed []string
	out, err := NewBuiltinMoaPlugin().ExecuteWithStream(context.Background(),
		[]string{`{"cmd":"ask","args":{"prompt":"q","models":["a","b"],"aggregator":"c","layers":2,"positions":"full"}}`},
		func(s string) { streamed = append(streamed, s) })
	if err != nil || out != "layered" {
		t.Fatalf("ask = %q, %v", out, err)
	}
	want := MoaRequest{Prompt: "q", Members: []string{"a", "b"}, Aggregator: "c", Layers: 2, Positions: "full"}
	if r.req.Prompt != want.Prompt || r.req.Layers != 2 || r.req.Positions != "full" || r.req.Aggregator != "c" || len(r.req.Members) != 2 {
		t.Fatalf("request = %+v, want %+v", r.req, want)
	}
	if len(streamed) != 1 || streamed[0] != "round 1/2" {
		t.Fatalf("progress should stream to the tool output, got %v", streamed)
	}
	p := NewBuiltinMoaPlugin()
	if !strings.Contains(p.Schema(), `"layers"`) || !strings.Contains(p.Schema(), `"positions"`) || !strings.Contains(p.Usage(), "layers") {
		t.Fatal("schema and usage should document layers and positions")
	}
}

// The agent loop flattens a {"cmd","args"} envelope into argv before the
// tool runs, so flag values arrive as strings; layers must survive that.
func TestMoa_AskLayersSurviveTheAgentArgvForm(t *testing.T) {
	cases := map[string][]string{
		"argv":           {"ask", "--layers", "2", "--positions", "lead", "--prompt", "q"},
		"argv equals":    {"ask", "--layers=3", "--prompt", "q"},
		"string in json": {`{"cmd":"ask","args":{"prompt":"q","layers":"2"}}`},
		"float in json":  {`{"cmd":"ask","args":{"prompt":"q","layers":2.0}}`},
	}
	want := map[string]int{"argv": 2, "argv equals": 3, "string in json": 2, "float in json": 2}
	for name, args := range cases {
		r := &fakeMoaRunner{}
		SetMoaAdapter(r)
		if _, err := NewBuiltinMoaPlugin().Execute(context.Background(), args); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if r.req.Layers != want[name] || r.req.Prompt != "q" {
			t.Errorf("%s: request = %+v, want layers %d", name, r.req, want[name])
		}
	}
	SetMoaAdapter(nil)
}
