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

// fakeCompressionAdapter scripts the CompressionAdapter contract for tests.
type fakeCompressionAdapter struct {
	store      map[string]string
	lastHint   string
	compressed string
}

func (f *fakeCompressionAdapter) Recall(key string) (string, bool) {
	v, ok := f.store[key]
	return v, ok
}
func (f *fakeCompressionAdapter) Compress(hint, content string) (string, error) {
	f.lastHint = hint
	return f.compressed, nil
}
func (f *fakeCompressionAdapter) Stats() string { return "savings: 42%" }

func withAdapter(a CompressionAdapter) func() {
	SetCompressionAdapter(a)
	return func() { SetCompressionAdapter(nil) }
}

func TestRecallRequiresAdapter(t *testing.T) {
	defer withAdapter(nil)()
	SetCompressionAdapter(nil)
	p := NewBuiltinRecallPlugin()
	if _, err := p.Execute(context.Background(), []string{`{"key":"abc"}`}); err == nil {
		t.Fatal("expected error when no adapter is wired")
	}
}

func TestRecallReturnsOriginal(t *testing.T) {
	fa := &fakeCompressionAdapter{store: map[string]string{"recall-key-1": "THE FULL ORIGINAL"}}
	defer withAdapter(fa)()
	p := NewBuiltinRecallPlugin()

	// Bare key.
	out, err := p.Execute(context.Background(), []string{`{"key":"recall-key-1"}`})
	if err != nil || out != "THE FULL ORIGINAL" {
		t.Fatalf("bare key recall failed: out=%q err=%v", out, err)
	}
	// Full marker form.
	out, err = p.Execute(context.Background(), []string{`{"key":"<<ccr:recall-key-1>>"}`})
	if err != nil || out != "THE FULL ORIGINAL" {
		t.Fatalf("marker-form recall failed: out=%q err=%v", out, err)
	}
}

func TestRecallUnknownKey(t *testing.T) {
	fa := &fakeCompressionAdapter{store: map[string]string{}}
	defer withAdapter(fa)()
	p := NewBuiltinRecallPlugin()
	if _, err := p.Execute(context.Background(), []string{`{"key":"missing-key-x"}`}); err == nil {
		t.Fatal("expected error for unknown key")
	}
}

func TestCompressDispatch(t *testing.T) {
	fa := &fakeCompressionAdapter{compressed: "SMALL"}
	defer withAdapter(fa)()
	p := NewBuiltinCompressPlugin()

	out, err := p.Execute(context.Background(), []string{`{"content":"big log here","hint":"log"}`})
	if err != nil || out != "SMALL" {
		t.Fatalf("compress failed: out=%q err=%v", out, err)
	}
	if fa.lastHint != "log" {
		t.Fatalf("hint not forwarded: %q", fa.lastHint)
	}
}

func TestCompressAutoHintNormalizedToEmpty(t *testing.T) {
	fa := &fakeCompressionAdapter{compressed: "x"}
	defer withAdapter(fa)()
	p := NewBuiltinCompressPlugin()
	_, _ = p.Execute(context.Background(), []string{`{"content":"data","hint":"auto"}`})
	if fa.lastHint != "" {
		t.Fatalf("auto hint should normalize to empty, got %q", fa.lastHint)
	}
}

func TestCompressStatsSubcommand(t *testing.T) {
	fa := &fakeCompressionAdapter{}
	defer withAdapter(fa)()
	p := NewBuiltinCompressPlugin()
	out, err := p.Execute(context.Background(), []string{`{"cmd":"stats"}`})
	if err != nil || !strings.Contains(out, "savings") {
		t.Fatalf("stats subcommand failed: out=%q err=%v", out, err)
	}
}

func TestCompressBareTextIsContent(t *testing.T) {
	fa := &fakeCompressionAdapter{compressed: "ok"}
	defer withAdapter(fa)()
	p := NewBuiltinCompressPlugin()
	if _, err := p.Execute(context.Background(), []string{"just", "raw", "text"}); err != nil {
		t.Fatalf("bare text should be treated as content: %v", err)
	}
}
