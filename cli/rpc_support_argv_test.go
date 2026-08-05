/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/diillson/chatcli/cli/plugins"
	"go.uber.org/zap"
)

// argvRecorderPlugin records the argv it was executed with.
type argvRecorderPlugin struct{ gotArgs []string }

func (p *argvRecorderPlugin) Name() string        { return "@argv-recorder" }
func (p *argvRecorderPlugin) Description() string { return "records argv" }
func (p *argvRecorderPlugin) Usage() string       { return "" }
func (p *argvRecorderPlugin) Version() string     { return "test" }
func (p *argvRecorderPlugin) Path() string        { return "" }
func (p *argvRecorderPlugin) Schema() string      { return "" }
func (p *argvRecorderPlugin) Execute(_ context.Context, args []string) (string, error) {
	p.gotArgs = append([]string(nil), args...)
	return "ok", nil
}
func (p *argvRecorderPlugin) ExecuteWithStream(ctx context.Context, args []string, _ func(string)) (string, error) {
	return p.Execute(ctx, args)
}

func newTestPluginManagerWith(t *testing.T, p plugins.Plugin) *plugins.Manager {
	t.Helper()
	mgr, err := plugins.NewManager(zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	mgr.RegisterBuiltinPlugin(p)
	return mgr
}

// TestRunAnyRPCToolArgv_Guards mirrors TestRunAnyRPCTool_Guards for the
// argv-native entry: no plugin manager and unknown tools must error, never
// panic. The argv form exists so callers with a real argv (the `chatcli
// tool` subcommand) never join-and-resplit their arguments.
func TestRunAnyRPCToolArgv_Guards(t *testing.T) {
	c := &ChatCLI{}
	if _, err := c.RunAnyRPCToolArgv(context.Background(), "read", []string{"--file", "x"}); err == nil {
		t.Fatal("nil pluginManager must error")
	}
}

// TestRunAnyRPCToolArgv_PreservesArgs proves argv reaches the plugin
// verbatim: a value containing whitespace stays one argument.
func TestRunAnyRPCToolArgv_PreservesArgs(t *testing.T) {
	rec := &argvRecorderPlugin{}
	c := &ChatCLI{pluginManager: newTestPluginManagerWith(t, rec)}
	t.Setenv("CHATCLI_MCP_TOOLS", "all")
	if _, err := c.RunAnyRPCToolArgv(context.Background(), rec.Name(), []string{"--term", "two words"}); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(rec.gotArgs) != 2 || rec.gotArgs[1] != "two words" {
		t.Fatalf("argv not preserved: %q", rec.gotArgs)
	}
	if strings.Contains(strings.Join(rec.gotArgs, "|"), "two|words") {
		t.Fatal("whitespace value was re-split")
	}
}
