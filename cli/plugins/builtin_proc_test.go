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

type fakeProcAdapter struct {
	lastCall string
	command  string
	dir      string
	id       string
	tail     int
}

func (f *fakeProcAdapter) Start(command, dir string) (string, error) {
	f.lastCall, f.command, f.dir = "start", command, dir
	return "started p1", nil
}
func (f *fakeProcAdapter) Status(id string) (string, error) {
	f.lastCall, f.id = "status", id
	return "status", nil
}
func (f *fakeProcAdapter) Logs(id string, tail int) (string, error) {
	f.lastCall, f.id, f.tail = "logs", id, tail
	return "logs", nil
}
func (f *fakeProcAdapter) Stop(id string) (string, error) {
	f.lastCall, f.id = "stop", id
	return "stopped", nil
}
func (f *fakeProcAdapter) Remove(id string) (string, error) {
	f.lastCall, f.id = "remove", id
	return "removed", nil
}
func (f *fakeProcAdapter) List() (string, error) {
	f.lastCall = "list"
	return "list", nil
}

func withFakeProc(t *testing.T) *fakeProcAdapter {
	t.Helper()
	fake := &fakeProcAdapter{}
	SetProcAdapter(fake)
	t.Cleanup(func() { SetProcAdapter(nil) })
	return fake
}

func TestProcEnvelopeDispatch(t *testing.T) {
	fake := withFakeProc(t)
	p := NewBuiltinProcPlugin()

	if _, err := p.Execute(context.Background(),
		[]string{`{"cmd":"start","args":{"command":"npm run dev","dir":"./web"}}`}); err != nil {
		t.Fatalf("start: %v", err)
	}
	if fake.lastCall != "start" || fake.command != "npm run dev" || fake.dir != "./web" {
		t.Fatalf("start args not threaded: %+v", fake)
	}

	if _, err := p.Execute(context.Background(),
		[]string{`{"cmd":"logs","args":{"id":"p1","tail":50}}`}); err != nil {
		t.Fatalf("logs: %v", err)
	}
	if fake.lastCall != "logs" || fake.id != "p1" || fake.tail != 50 {
		t.Fatalf("logs args not threaded: %+v", fake)
	}
}

func TestProcArgvFormsAndAliases(t *testing.T) {
	fake := withFakeProc(t)
	p := NewBuiltinProcPlugin()

	// argv start joins the remaining args into the command.
	if _, err := p.Execute(context.Background(), []string{"run", "go", "run", "./cmd/server"}); err != nil {
		t.Fatalf("argv start: %v", err)
	}
	if fake.lastCall != "start" || fake.command != "go run ./cmd/server" {
		t.Fatalf("argv start not threaded: %+v", fake)
	}

	for alias, want := range map[string]string{"tail": "logs", "kill": "stop", "ps": "list", "info": "status", "rm": "remove"} {
		args := []string{alias}
		if want != "list" {
			args = append(args, "p1")
		}
		if _, err := p.Execute(context.Background(), args); err != nil {
			t.Fatalf("alias %q: %v", alias, err)
		}
		if fake.lastCall != want {
			t.Fatalf("alias %q dispatched %q, want %q", alias, fake.lastCall, want)
		}
	}
}

func TestProcValidation(t *testing.T) {
	withFakeProc(t)
	p := NewBuiltinProcPlugin()

	cases := []struct {
		args []string
		want string
	}{
		{[]string{`{"cmd":"start","args":{}}`}, `"command" is required`},
		{[]string{`{"cmd":"logs","args":{}}`}, `"id" is required`},
		{[]string{`{"cmd":"restart","args":{}}`}, "unknown cmd"},
	}
	for _, c := range cases {
		if _, err := p.Execute(context.Background(), c.args); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Fatalf("args %v: err = %v, want contains %q", c.args, err, c.want)
		}
	}
	SetProcAdapter(nil)
	if _, err := p.Execute(context.Background(), []string{`{"cmd":"list"}`}); err == nil {
		t.Fatal("missing adapter must error, not panic")
	}
}

// TestProcCapsPerSubcommand pins the security posture: observation is
// read-only, lifecycle mutation is not — and unparseable args default to NOT
// read-only (fail closed).
func TestProcCapsPerSubcommand(t *testing.T) {
	p := NewBuiltinProcPlugin()

	readOnly := [][]string{
		{`{"cmd":"status","args":{"id":"p1"}}`},
		{`{"cmd":"logs","args":{"id":"p1"}}`},
		{`{"cmd":"list"}`},
	}
	for _, args := range readOnly {
		if !p.IsReadOnly(args) {
			t.Fatalf("%v must be read-only", args)
		}
	}
	mutating := [][]string{
		{`{"cmd":"start","args":{"command":"x"}}`},
		{`{"cmd":"stop","args":{"id":"p1"}}`},
		{`{"cmd":"remove","args":{"id":"p1"}}`},
		{`{not json`}, // fail closed
	}
	for _, args := range mutating {
		if p.IsReadOnly(args) {
			t.Fatalf("%v must NOT be read-only", args)
		}
	}
	if !p.IsConcurrencySafe(nil) {
		t.Fatal("@proc must advertise concurrency-safe")
	}
}
