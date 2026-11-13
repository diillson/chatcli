/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package plugins

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// fakeParkPruner is a park runtime that records what prune asked for.
type fakeParkPruner struct {
	fakeParkCanceller
	calls []string
	err   error
}

func (f *fakeParkPruner) PrunePark(olderThan string) (string, error) {
	f.calls = append(f.calls, olderThan)
	return "pruned 2 parked snapshot(s)", f.err
}

// @park prune in the argv the agent loop really delivers: the flattened
// {"cmd":"prune","args":{...}}. What is not understood never reaches the
// runtime.
func TestParkPruneFlattenedArgv(t *testing.T) {
	f := &fakeParkPruner{}
	SetParkCanceller(f)
	t.Cleanup(func() { SetParkCanceller(nil) })
	p := NewBuiltinParkPlugin()
	for argv, want := range map[string]string{
		"prune":                  "",
		"prune --older_than 24h": "24h",
		"gc --older_than 7d":     "7d",
		`{"cmd":"sweep","args":{"older_than":"36h"}}`: "36h",
	} {
		f.calls = nil
		out, err := p.Execute(context.Background(), strings.Fields(argv))
		if err != nil || out != "pruned 2 parked snapshot(s)" || len(f.calls) != 1 || f.calls[0] != want {
			t.Errorf("%q: out=%q err=%v calls=%q, want one call with %q", argv, out, err, f.calls, want)
		}
	}
	f.calls = nil
	for argv, want := range map[string]string{
		"prune --older_than 24":                    "has no unit",                   // {"older_than":24} flattened
		"prune --older_than":                       "expected a duration string",    // {"older_than":true} flattened
		"prune --token abc":                        `unknown argument "token"`,      // discard's argument
		"prune --force":                            `unknown argument "force"`,      // no way to take a live park
		"prune --older-than 24h":                   `unknown argument "older-than"`, // not the documented spelling
		"prune 24h":                                "unexpected positional",
		"prune --older_than 1h --x":                `unknown argument "x"`, // a valid age does not excuse the rest
		`{"cmd":"prune","args":{"older_than":""}}`: "older_than is empty",
	} {
		if out, err := p.Execute(context.Background(), strings.Fields(argv)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: got %q %v, want an error with %q", argv, out, err, want)
		}
	}
	if len(f.calls) != 0 {
		t.Fatalf("a rejected prune reached the runtime: %q", f.calls)
	}
	f.err = errors.New("older_than is not a positive age")
	if _, err := p.Execute(context.Background(), []string{"prune", "--older_than", "soon"}); err == nil || !strings.Contains(err.Error(), "@park: prune: older_than") {
		t.Errorf("a runtime refusal must surface: %v", err)
	}
	// A runtime that cannot prune (or none) refuses and names the command.
	for _, c := range []ParkCanceller{&fakeParkCanceller{}, nil} {
		SetParkCanceller(c)
		if _, err := p.Execute(context.Background(), []string{"prune"}); err == nil || !strings.Contains(err.Error(), "/parked prune") {
			t.Errorf("no pruner: %v", err)
		}
	}
	// The catalog is not loaded in this package: DescribeCall gives the key.
	if got := p.DescribeCall([]string{"prune"}); got != "leftovers.park.describe.prune" || p.IsReadOnly([]string{"prune"}) {
		t.Errorf("prune is a mutating verb with its own description: %q", got)
	}
	if !strings.Contains(p.Usage(), "prune    {older_than?") || !strings.Contains(p.Schema(), `"name":"prune"`) || !strings.Contains(p.Schema(), `"name":"older_than"`) {
		t.Error("prune and older_than must be in Usage and Schema")
	}
}

// scopedProcAdapter is a ProcAdapter that hands out one adapter per context.
type scopedProcAdapter struct {
	ProcAdapter
	name string
}

type procScopeKey struct{}

func (a *scopedProcAdapter) ForContext(ctx context.Context) ProcAdapter {
	name, _ := ctx.Value(procScopeKey{}).(string)
	return &scopedProcAdapter{name: name}
}
func (a *scopedProcAdapter) List() (string, error)                        { return "table of " + a.name, nil }
func (a *scopedProcAdapter) LogsGrep(string, string, int) (string, error) { return "", nil }
func (a *scopedProcAdapter) Wait(_ context.Context, id, _ string, timeoutSec int) (string, error) {
	return fmt.Sprintf("%s waits %ds on %s", a.name, timeoutSec, id), nil
}

// @proc asks the wired adapter for the one bound to the call's context, so
// every verb runs on the caller's own process table.
func TestProcToolBindsTheAdapterToTheCallContext(t *testing.T) {
	SetProcAdapter(&scopedProcAdapter{name: "unbound"})
	t.Cleanup(func() { SetProcAdapter(nil) })
	p := NewBuiltinProcPlugin()
	for _, session := range []string{"alice", "bob"} {
		ctx := context.WithValue(context.Background(), procScopeKey{}, session)
		if out, err := p.Execute(ctx, []string{"list"}); err != nil || out != "table of "+session {
			t.Errorf("list for %s: %q %v", session, out, err)
		}
		// {"cmd":"wait","args":{"id":"p1","timeout":90}} flattened: the number
		// arrives as text and must still be read as seconds.
		if out, err := p.Execute(ctx, []string{"wait", "--id", "p1", "--timeout", "90"}); err != nil || out != session+" waits 90s on p1" {
			t.Errorf("wait for %s: %q %v", session, out, err)
		}
	}
}
