/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package plugins

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/pkg/pulse"
	"github.com/diillson/chatcli/pkg/pulse/pulsetest"
)

// parentRecordingDescriber is fakeDescriber that also records the
// innermost dashboard parent each request carried.
type parentRecordingDescriber struct {
	fakeDescriber
	mu      sync.Mutex
	parents []string
}

func (d *parentRecordingDescriber) DescribeImage(ctx context.Context, prompt string, img models.ImageContent) (string, string, error) {
	d.mu.Lock()
	d.parents = append(d.parents, pulse.ParentFrom(ctx))
	d.mu.Unlock()
	return d.fakeDescriber.DescribeImage(ctx, prompt, img)
}

func TestDocsFlattenShowsOnDashboard(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // private vision cache
	root := writeDocsFixture(t)
	fake := &parentRecordingDescriber{}
	SetDocsImageDescriber(fake)
	t.Cleanup(func() { SetDocsImageDescriber(nil) })
	rec := pulsetest.Record(t)

	flattenFixture(t, `{"root":"`+filepath.ToSlash(root)+`","format":"jsonl","output":"OUT","images":"describe"}`)

	start := rec.WaitFor(t, "flatten start", pulsetest.Is(pulse.KindTool, pulseDocsFlatten, pulse.PhaseStart))
	ev := rec.WaitFor(t, "flatten end", pulsetest.Is(pulse.KindTool, pulseDocsFlatten, pulse.PhaseEnd))
	want := map[string]string{
		"source": "local", "kind": "docs", "format": "jsonl", "images_mode": "describe",
		"images": "1", "images_described": "1", "images_cached": "0", "describer": "fake/vision-1",
	}
	for k, v := range want {
		if ev.Attrs[k] != v {
			t.Errorf("attr %s = %q, want %q (all %v)", k, ev.Attrs[k], v, ev.Attrs)
		}
	}
	if ev.Attrs["files"] == "" || ev.Attrs["chunks"] == "" || ev.Status != pulse.StatusOK {
		t.Errorf("end = %+v", ev)
	}
	if len(fake.parents) != 1 || fake.parents[0] != start.ID {
		t.Errorf("image descriptions must hang from the flatten node %q, got %v", start.ID, fake.parents)
	}
	for _, e := range rec.Events() {
		for _, v := range e.Attrs {
			if strings.Contains(v, root) || strings.Contains(v, "arch.png") {
				t.Fatalf("a path leaked onto the bus: %q", v)
			}
		}
	}
}

func TestDocsFlattenFailureEndsAsError(t *testing.T) {
	rec := pulsetest.Record(t)
	_, err := NewBuiltinDocsFlattenPlugin().Execute(context.Background(),
		[]string{`{"root":"` + filepath.ToSlash(filepath.Join(t.TempDir(), "missing")) + `"}`})
	if err == nil {
		t.Fatal("expected an error for a missing root")
	}
	ev := rec.WaitFor(t, "failed flatten", pulsetest.Is(pulse.KindTool, pulseDocsFlatten, pulse.PhaseEnd))
	if ev.Status != pulse.StatusError {
		t.Errorf("status %q, want error", ev.Status)
	}
}
