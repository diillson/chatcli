/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package workers

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/diillson/chatcli/cli/agent/runs"
	"github.com/diillson/chatcli/pkg/pulse"
	"github.com/diillson/chatcli/pkg/pulse/pulsetest"
)

type fakeEngine struct {
	err    error
	parent string
}

func (f *fakeEngine) Execute(ctx context.Context, _ string, _ []string) error {
	f.parent = pulse.ParentFrom(ctx)
	return f.err
}

// A direct engine call is a @coder tool node under the worker's run, with
// its subcommand and outcome, and what it starts hangs from it. Arguments
// never travel.
func TestDirectExecuteIsAToolNodeUnderTheRun(t *testing.T) {
	rec := pulsetest.Record(t)
	ctx, run := runs.NewRegistry(4).Begin(context.Background(), runs.Info{Kind: runs.KindWorker, Agent: "git"})
	defer run.End(nil)

	fe := &fakeEngine{err: errors.New("exit status 1")}
	if err := directExecute(ctx, fe, "exec", []string{"--cmd", "SECRET-CMD"}); err == nil {
		t.Fatal("the engine error must come back")
	}
	ev := rec.WaitFor(t, "direct tool end", pulsetest.Is(pulse.KindTool, "@coder", pulse.PhaseEnd))
	if ev.Parent != run.ID() || ev.Status != pulse.StatusError || ev.Attrs["subcmd"] != "exec" || ev.Attrs["origin"] != pulseDirectOrigin {
		t.Fatalf("direct tool node: %+v", ev)
	}
	if fe.parent != ev.ID {
		t.Fatalf("the engine must run under the tool node: %q vs %q", fe.parent, ev.ID)
	}
	for _, v := range ev.Attrs {
		if v == "SECRET-CMD" {
			t.Fatalf("argument leaked: %+v", ev.Attrs)
		}
	}
}

// A real skill script reports each engine call it makes.
func TestSkillScriptEngineCallsReachTheDashboard(t *testing.T) {
	rec := pulsetest.Record(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n\ntype T struct{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, run := runs.NewRegistry(4).Begin(context.Background(), runs.Info{Kind: runs.KindWorker, Agent: "search"})
	defer run.End(nil)
	if _, err := mapProjectScript(ctx, map[string]string{"dir": dir}, nil); err != nil {
		t.Fatalf("mapProjectScript: %v", err)
	}
	rec.WaitFor(t, "tree call", func(ev pulse.Event) bool {
		return pulsetest.Is(pulse.KindTool, "@coder", pulse.PhaseEnd)(ev) && ev.Attrs["subcmd"] == "tree" && ev.Parent == run.ID()
	})
	rec.WaitFor(t, "search call", func(ev pulse.Event) bool {
		return pulsetest.Is(pulse.KindTool, "@coder", pulse.PhaseEnd)(ev) && ev.Attrs["subcmd"] == "search" && ev.Parent == run.ID()
	})
}
