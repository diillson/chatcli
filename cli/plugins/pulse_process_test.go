/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package plugins

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/diillson/chatcli/pkg/pulse"
	"github.com/diillson/chatcli/pkg/pulse/pulsetest"
)

func scriptPlugin(t *testing.T, body string) *ExecutablePlugin {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script plugin fixture")
	}
	path := filepath.Join(t.TempDir(), "tp")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return &ExecutablePlugin{metadata: Metadata{Name: "@tp"}, path: path}
}

// The process a plugin spawns is a child of the tool call that ran it, with
// its exit code and whether the verified copy ran — never its arguments.
func TestPluginProcessIsAChildNodeWithItsExit(t *testing.T) {
	rec := pulsetest.Record(t)
	p := scriptPlugin(t, "echo out; exit 3")
	ctx := pulse.WithParent(context.Background(), "tool-5")
	if _, err := p.Execute(ctx, []string{"SECRET-ARG"}); err == nil {
		t.Fatal("exit 3 must fail")
	}
	ev := rec.WaitFor(t, "plugin process end", pulsetest.Is(pulse.KindTool, pulsePluginProcessNode, pulse.PhaseEnd))
	if ev.Parent != "tool-5" || ev.Status != pulse.StatusError {
		t.Fatalf("process node: %+v", ev)
	}
	want := map[string]string{"plugin": "@tp", "verified": "false", "exit_code": "3", "timeout": "false"}
	for k, v := range want {
		if ev.Attrs[k] != v {
			t.Fatalf("attr %s = %q, want %q (%+v)", k, ev.Attrs[k], v, ev.Attrs)
		}
	}
	for _, v := range ev.Attrs {
		if strings.Contains(v, "SECRET") || strings.Contains(v, string(filepath.Separator)) {
			t.Fatalf("argument or path leaked: %+v", ev.Attrs)
		}
	}
}

// A plugin killed by its deadline reports the timeout and the signal.
func TestPluginProcessKilledByDeadline(t *testing.T) {
	t.Setenv("CHATCLI_AGENT_PLUGIN_TIMEOUT", "100ms")
	rec := pulsetest.Record(t)
	p := scriptPlugin(t, "exec sleep 5")
	_, _ = p.Execute(context.Background(), nil)
	ev := rec.WaitFor(t, "plugin process end", pulsetest.Is(pulse.KindTool, pulsePluginProcessNode, pulse.PhaseEnd))
	if ev.Status != pulse.StatusCancelled || ev.Attrs["timeout"] != "true" || ev.Attrs["signal"] != "killed" {
		t.Fatalf("killed process: %+v", ev)
	}
}

// A clean exit ends ok with exit code 0.
func TestPluginProcessCleanExit(t *testing.T) {
	rec := pulsetest.Record(t)
	p := scriptPlugin(t, "echo fine")
	if _, err := p.Execute(context.Background(), nil); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	ev := rec.WaitFor(t, "plugin process end", pulsetest.Is(pulse.KindTool, pulsePluginProcessNode, pulse.PhaseEnd))
	if ev.Status != pulse.StatusOK || ev.Attrs["exit_code"] != "0" || ev.DurMS == 0 {
		t.Fatalf("clean process: %+v", ev)
	}
}
