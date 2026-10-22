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

// fakePausingAdapter is fakeAdapter plus the SchedulerPauser capability.
type fakePausingAdapter struct{ fakeAdapter }

func (f *fakePausingAdapter) PauseJob(_ context.Context, _ SchedulerOwner, in string) (string, error) {
	f.lastCmd, f.lastInner = "pause", in
	return "{\"ok\":true,\"status\":\"paused\"}", nil
}
func (f *fakePausingAdapter) ResumeJob(_ context.Context, _ SchedulerOwner, in string) (string, error) {
	f.lastCmd, f.lastInner = "resume", in
	return "{\"ok\":true,\"status\":\"pending\"}", nil
}

func TestSchedulerPauseResume(t *testing.T) {
	prev := currentSchedulerAdapter()
	f := &fakePausingAdapter{}
	SetSchedulerAdapter(f)
	t.Cleanup(func() { SetSchedulerAdapter(prev) })
	p := NewBuiltinSchedulerPlugin()
	ctx := context.Background()

	out, err := p.Execute(ctx, []string{`{"cmd":"pause","args":{"id":"job-1","reason":"waiting for the fix"}}`})
	if err != nil || !strings.Contains(out, "paused") || f.lastCmd != "pause" || parseInner(t, f.lastInner)["id"] != "job-1" {
		t.Fatalf("pause: %q %v %+v", out, err, f.fakeAdapter)
	}
	if _, err := p.Execute(ctx, []string{"hold", "--id", "job-1"}); err != nil || f.lastCmd != "pause" {
		t.Fatalf("pause alias: %v %s", err, f.lastCmd)
	}
	out, err = p.Execute(ctx, []string{`{"cmd":"resume","args":{"id":"job-1"}}`})
	if err != nil || !strings.Contains(out, "pending") || f.lastCmd != "resume" {
		t.Fatalf("resume: %q %v", out, err)
	}
	if _, err := p.Execute(ctx, []string{`{"cmd":"unpause","args":{"id":"job-1"}}`}); err != nil || f.lastCmd != "resume" {
		t.Fatalf("resume alias: %v", err)
	}
	for _, args := range [][]string{{`{"cmd":"pause","args":{"id":"j"}}`}, {`{"cmd":"resume","args":{"id":"j"}}`}} {
		if p.IsReadOnly(args) {
			t.Fatalf("%v mutates the schedule", args)
		}
		if d := p.DescribeCall(args); !strings.Contains(d, "j") {
			t.Fatalf("describe %v = %q", args, d)
		}
	}
	// Without the capability the verb says so instead of enqueueing.
	plain := &fakeAdapter{}
	SetSchedulerAdapter(plain)
	if _, err := p.Execute(ctx, []string{`{"cmd":"pause","args":{"id":"j"}}`}); err == nil || !strings.Contains(err.Error(), "not available") || plain.lastCmd != "" {
		t.Fatalf("pause without capability: %v %q", err, plain.lastCmd)
	}
}
