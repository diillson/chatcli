/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/diillson/chatcli/cli/plugins"
	"go.uber.org/zap"
)

func TestSynthesizePluginDefFromSchema(t *testing.T) {
	// @forge has a rich Schema() with subcommands but no native def.
	def, ok := synthesizePluginDef("forge", plugins.NewBuiltinForgePlugin())
	if !ok {
		t.Fatal("forge schema must synthesize a def")
	}
	if def.Function.Name != "forge" {
		t.Fatalf("def name: %q", def.Function.Name)
	}
	params, _ := json.Marshal(def.Function.Parameters)
	var parsed struct {
		Properties struct {
			Cmd struct {
				Enum []string `json:"enum"`
			} `json:"cmd"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	if json.Unmarshal(params, &parsed) != nil {
		t.Fatal("params must be valid JSON schema")
	}
	if len(parsed.Properties.Cmd.Enum) == 0 {
		t.Fatal("cmd enum must list subcommands")
	}
	if len(parsed.Required) != 1 || parsed.Required[0] != "cmd" {
		t.Fatalf("cmd must be required: %v", parsed.Required)
	}
}

func TestNativeNameForBuiltin(t *testing.T) {
	if nativeNameForBuiltin("@websearch") != "web_search" {
		t.Fatal("websearch → web_search")
	}
	if nativeNameForBuiltin("@browser") != "" {
		t.Fatal("browser has no native mapping")
	}
}

func TestWorkerPluginToolDefsFiltersAndSynthesizes(t *testing.T) {
	mgr, _ := plugins.NewManager(zap.NewNop())
	cli := &ChatCLI{pluginManager: mgr}
	cli.pluginManager.RegisterBuiltinPlugin(plugins.NewBuiltinForgePlugin())
	defs := cli.workerPluginToolDefs([]string{"@websearch", "@forge", "mcp_nope"})
	names := map[string]bool{}
	for _, d := range defs {
		names[d.Function.Name] = true
	}
	if !names["websearch"] {
		t.Fatal("mapped builtin @websearch must appear under its grant def name")
	}
	if !names["forge"] {
		t.Fatal("unmapped builtin @forge must be synthesized as forge")
	}
	if names["mcp_nope"] {
		t.Fatal("unknown mcp tool must be dropped (no mcp manager)")
	}
}

func TestRunWorkerPluginToolSerializesUnsafe(t *testing.T) {
	mgr, _ := plugins.NewManager(zap.NewNop())
	cli := &ChatCLI{pluginManager: mgr}
	// @browser is IsConcurrencySafe=false → runWorkerPluginTool must take
	// the per-plugin lock. Without a real browser the call errors, but the
	// routing (GetPlugin + lock path) is what we exercise.
	cli.pluginManager.RegisterBuiltinPlugin(plugins.NewBuiltinBrowserPlugin())
	_, err := cli.runWorkerPluginTool(context.Background(), "@browser", `{"cmd":"status"}`)
	_ = err // may error without Chrome; must not panic/deadlock
	// Unknown plugin surfaces a clean error, never a panic.
	if _, err := cli.runWorkerPluginTool(context.Background(), "@nope", "{}"); err == nil {
		t.Fatal("unknown plugin must error")
	}
	// mcp_ without a manager errors cleanly.
	if _, err := cli.runWorkerPluginTool(context.Background(), "mcp_x", "{}"); err == nil {
		t.Fatal("mcp_ without manager must error")
	}
}
