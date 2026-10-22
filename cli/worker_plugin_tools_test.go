/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"context"
	"encoding/json"
	"strings"
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

func TestSynthesizePluginDef_FlatForSingleVerbAndTypedUnionForMany(t *testing.T) {
	// @read declares one subcommand that is the tool itself → flat params.
	def, ok := synthesizePluginDef("read", plugins.NewBuiltinReadPlugin())
	if !ok {
		t.Fatal("read must synthesize")
	}
	raw, _ := json.Marshal(def.Function.Parameters)
	if strings.Contains(string(raw), `"cmd"`) || !strings.Contains(string(raw), `"file"`) || !strings.Contains(string(raw), `"required":["file"]`) {
		t.Fatalf("single-verb tool must be flat and typed: %s", raw)
	}
	// @osv has two subcommands → envelope with cmd enum and typed args union.
	def, ok = synthesizePluginDef("osv", plugins.NewBuiltinOsvPlugin())
	if !ok {
		t.Fatal("osv must synthesize")
	}
	raw, _ = json.Marshal(def.Function.Parameters)
	for _, want := range []string{`"enum":["scan","check"]`, `"path":{`, `"ecosystem":{`, `"version":{`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("multi-verb tool must type the union of its flags (%s): %s", want, raw)
		}
	}
	if !strings.Contains(string(raw), "(check)") || !strings.Contains(string(raw), "(scan)") {
		t.Fatalf("a flag used by some subcommands must say which: %s", raw)
	}
	if !strings.Contains(def.Function.Description, "scan (") || !strings.Contains(def.Function.Description, "args: path?") || !strings.Contains(def.Function.Description, "args: ecosystem, package, version") {
		t.Fatalf("description must list each subcommand with its args: %q", def.Function.Description)
	}
	// A numeric default in a schema must not break synthesis.
	var tolerant nativeSchemaFlag
	if err := json.Unmarshal([]byte(`{"name":"depth","type":"int","default":3}`), &tolerant); err != nil || tolerant.Name != "depth" {
		t.Fatalf("numeric default must parse: %v", err)
	}
	if flagNames([]nativeSchemaFlag{{Name: "a", Required: true}, {Name: "b"}}) != "a, b?" {
		t.Fatal("flagNames marks optional flags")
	}
}
