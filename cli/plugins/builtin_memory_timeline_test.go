/*
 * ChatCLI - @memory timeline subcommand tests.
 * Copyright (c) 2024 Edilson Freitas. License: Apache-2.0.
 */
package plugins

import (
	"context"
	"strings"
	"testing"
)

// timelineFakeAdapter implements MemoryAdapter + TimelineAccessor and records
// the arguments the plugin dispatched.
type timelineFakeAdapter struct {
	project, from, to, query string
	limit                    int
}

func (f *timelineFakeAdapter) Remember(string, string) (string, error)         { return "", nil }
func (f *timelineFakeAdapter) UpdateProfile(map[string]string) (string, error) { return "", nil }
func (f *timelineFakeAdapter) Forget(string) (string, error)                   { return "", nil }
func (f *timelineFakeAdapter) Recall(string) (string, error)                   { return "", nil }
func (f *timelineFakeAdapter) Timeline(project, from, to, query string, limit int) (string, error) {
	f.project, f.from, f.to, f.query, f.limit = project, from, to, query, limit
	return "timeline-ok", nil
}

// bareAdapter implements only MemoryAdapter — no timeline capability.
type bareAdapter struct{}

func (bareAdapter) Remember(string, string) (string, error)         { return "", nil }
func (bareAdapter) UpdateProfile(map[string]string) (string, error) { return "", nil }
func (bareAdapter) Forget(string) (string, error)                   { return "", nil }
func (bareAdapter) Recall(string) (string, error)                   { return "", nil }

func TestBuiltinMemory_TimelineDispatch(t *testing.T) {
	fake := &timelineFakeAdapter{}
	SetMemoryAdapter(fake)
	defer SetMemoryAdapter(nil)

	p := NewBuiltinMemoryPlugin()
	out, err := p.Execute(context.Background(),
		[]string{`{"cmd":"timeline","args":{"project":"chatcli","from":"2026-04","to":"2026-06","query":"oauth","limit":"20"}}`})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != "timeline-ok" {
		t.Errorf("unexpected output %q", out)
	}
	if fake.project != "chatcli" || fake.from != "2026-04" || fake.to != "2026-06" || fake.query != "oauth" {
		t.Errorf("args not threaded: %+v", fake)
	}
	if fake.limit != 20 {
		t.Errorf("string limit must parse leniently to 20, got %d", fake.limit)
	}
}

func TestBuiltinMemory_TimelineAliases(t *testing.T) {
	fake := &timelineFakeAdapter{}
	SetMemoryAdapter(fake)
	defer SetMemoryAdapter(nil)

	p := NewBuiltinMemoryPlugin()
	for _, alias := range []string{"history", "episodes", "chronology"} {
		if _, err := p.Execute(context.Background(), []string{`{"cmd":"` + alias + `","args":{}}`}); err != nil {
			t.Errorf("alias %q must dispatch to timeline: %v", alias, err)
		}
	}
}

func TestBuiltinMemory_TimelineWithoutCapability(t *testing.T) {
	SetMemoryAdapter(bareAdapter{})
	defer SetMemoryAdapter(nil)

	p := NewBuiltinMemoryPlugin()
	_, err := p.Execute(context.Background(), []string{`{"cmd":"timeline","args":{}}`})
	if err == nil || !strings.Contains(err.Error(), "not available") {
		t.Errorf("adapter without TimelineAccessor must degrade gracefully, got %v", err)
	}
}

func TestLenientInt(t *testing.T) {
	cases := []struct {
		raw  string
		want int
	}{
		{`15`, 15}, {`"20"`, 20}, {`12.0`, 12}, {`"x"`, 0}, {``, 0}, {`null`, 0},
	}
	for _, c := range cases {
		if got := lenientInt([]byte(c.raw)); got != c.want {
			t.Errorf("lenientInt(%q) = %d, want %d", c.raw, got, c.want)
		}
	}
}
