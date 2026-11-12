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

// cleanerAdapter is fakeAdapter plus the optional clear capability.
type cleanerAdapter struct {
	fakeAdapter
}

func (c *cleanerAdapter) ClearJobs(_ context.Context, _ SchedulerOwner, in string) (string, error) {
	c.lastCmd, c.lastInner = "clear", in
	return `{"ok":true}`, nil
}

// The schema teaches wait {every, timeout, then}; the engine reads poll,
// wait_timeout and do, and reads "timeout" as the ACTION timeout. Without
// the mapping a model's interval and wait budget were silently dropped.
func TestExecute_WaitVocabularyReachesTheEngine(t *testing.T) {
	f := withFakeAdapter(t)
	p := NewBuiltinSchedulerPlugin()
	_, err := p.Execute(context.Background(), []string{
		`{"cmd":"wait","args":{"until":"tcp://db:5432","every":"5s","timeout":"2m","then":"shell: make migrate","action_timeout":"90s","max_block":"30s","notify":true}}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	got := parseInner(t, f.lastInner)
	want := map[string]any{
		"poll": "5s", "wait_timeout": "2m", "do": "shell: make migrate",
		"timeout": "90s", "max_block": "30s", "notify": true, "until": "tcp://db:5432",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %v, want %v (inner: %s)", k, got[k], v, f.lastInner)
		}
	}
	for _, gone := range []string{"every", "then", "action_timeout"} {
		if _, ok := got[gone]; ok {
			t.Errorf("%s leaked to the engine, which ignores it: %s", gone, f.lastInner)
		}
	}
	// Engine names given explicitly win over the taught ones.
	_, _ = p.Execute(context.Background(), []string{
		`{"cmd":"wait","args":{"until":"tcp://db:5432","timeout":"2m","wait_timeout":"9m","every":"5s","poll":"1s"}}`,
	})
	got = parseInner(t, f.lastInner)
	if got["wait_timeout"] != "9m" || got["poll"] != "1s" {
		t.Errorf("explicit engine names overridden: %s", f.lastInner)
	}
	// The argv form the sanitizer produces carries hyphenated flags.
	_, err = p.Execute(context.Background(), []string{"wait", "--until", "tcp://db:5432", "--every", "5s", "--max-block", "30s", "--on-timeout", "fire_anyway"})
	if err != nil {
		t.Fatal(err)
	}
	got = parseInner(t, f.lastInner)
	if got["poll"] != "5s" || got["max_block"] != "30s" || got["on_timeout"] != "fire_anyway" {
		t.Errorf("argv form: %s", f.lastInner)
	}
}

func TestExecute_ScheduleEvery(t *testing.T) {
	f := withFakeAdapter(t)
	p := NewBuiltinSchedulerPlugin()
	_, _ = p.Execute(context.Background(), []string{`{"cmd":"schedule","args":{"name":"hb","every":"30s","do":"noop"}}`})
	if got := parseInner(t, f.lastInner); got["when"] != "every 30s" {
		t.Errorf("every with no when must be the recurrence: %s", f.lastInner)
	}
	_, _ = p.Execute(context.Background(), []string{`{"cmd":"schedule","args":{"name":"g","when":"+1m","until":"tcp://a:1","every":"2s","do":"noop"}}`})
	if got := parseInner(t, f.lastInner); got["when"] != "+1m" || got["poll"] != "2s" {
		t.Errorf("every next to when must be the poll interval: %s", f.lastInner)
	}
}

func TestExecute_Clear(t *testing.T) {
	p := NewBuiltinSchedulerPlugin()
	// An adapter without the capability says so instead of doing nothing.
	withFakeAdapter(t)
	if _, err := p.Execute(context.Background(), []string{`{"cmd":"clear"}`}); err == nil || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("clear without the capability: %v", err)
	}
	c := &cleanerAdapter{}
	prev := currentSchedulerAdapter()
	SetSchedulerAdapter(c)
	t.Cleanup(func() { SetSchedulerAdapter(prev) })
	for _, alias := range []string{"clear", "prune", "clean"} {
		if _, err := p.Execute(context.Background(), []string{`{"cmd":"` + alias + `","args":{"statuses":["failed"],"older_than":"24h"}}`}); err != nil {
			t.Fatal(err)
		}
		got := parseInner(t, c.lastInner)
		filter, _ := got["filter"].(map[string]any)
		if c.lastCmd != "clear" || got["older_than"] != "24h" || filter == nil || filter["statuses"] == nil {
			t.Fatalf("%s: %s", alias, c.lastInner)
		}
	}
	if p.IsReadOnly([]string{`{"cmd":"clear"}`}) {
		t.Fatal("clear changes the scheduler's history; it is not read-only")
	}
}

// What the model is shown must describe what the tool does now.
func TestSchedulerSurfaceDescribesBackgroundWaits(t *testing.T) {
	p := NewBuiltinSchedulerPlugin()
	for _, want := range []string{"max_block", "async", "notify", "clear", "older_than", "on_timeout", "then"} {
		if !strings.Contains(p.Usage(), want) {
			t.Errorf("Usage lacks %q", want)
		}
		if !strings.Contains(p.Schema(), want) {
			t.Errorf("Schema lacks %q", want)
		}
	}
	if p.Version() != "1.3.0" {
		t.Errorf("version %s: the surface changed, bump it", p.Version())
	}
}
