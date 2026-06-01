package plugins

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestBuiltinAsk_Metadata(t *testing.T) {
	p := NewBuiltinAskPlugin()
	if p.Name() != "@ask" {
		t.Errorf("name = %q", p.Name())
	}
	if !json.Valid([]byte(p.Schema())) {
		t.Error("schema is not valid JSON")
	}
	if p.IsConcurrencySafe(nil) {
		t.Error("@ask must not be concurrency-safe (owns the TTY)")
	}
	if !p.IsReadOnly(nil) {
		t.Error("@ask should be read-only")
	}
}

func TestBuiltinAsk_FallbackPicksFirst(t *testing.T) {
	p := NewBuiltinAskPlugin()
	args := []string{`{"questions":[{"header":"DB","question":"q","options":[{"label":"Postgres"},{"label":"SQLite"}]}]}`}
	out, err := p.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "Postgres") || !strings.Contains(out, "No interactive UI") {
		t.Errorf("fallback output unexpected: %s", out)
	}
}

func TestBuiltinAsk_BadArgs(t *testing.T) {
	p := NewBuiltinAskPlugin()
	_, err := p.Execute(context.Background(), []string{`{"questions":[]}`})
	if err == nil {
		t.Fatal("expected error for empty questions")
	}
}

func TestBuiltinAsk_DescribeCall(t *testing.T) {
	p := NewBuiltinAskPlugin()
	args := []string{`{"questions":[{"header":"A","question":"q","options":[{"label":"x"}]},{"header":"B","question":"q","options":[{"label":"y"}]}]}`}
	if d := p.DescribeCall(args); d == "" {
		t.Error("DescribeCall returned empty")
	}
}
