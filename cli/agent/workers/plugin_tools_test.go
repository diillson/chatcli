/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package workers

import (
	"context"
	"testing"

	"github.com/diillson/chatcli/models"
)

// AgentCall and WorkerDeps MUST stay comparable (Floor 13). This fails to
// compile if a slice/map/func field ever sneaks in.
func TestAgentCallStaysComparable(t *testing.T) {
	_ = map[AgentCall]bool{}
	_ = map[WorkerDeps]bool{}
	a := AgentCall{Agent: "coder", ID: "x"}
	b := AgentCall{Agent: "coder", ID: "x"}
	if a != b {
		t.Fatal("equal AgentCalls must compare equal")
	}
}

func TestNormalizePluginGrant(t *testing.T) {
	got := NormalizePluginGrant([]string{" Browser ", "@websearch", "browser", "mcp_search", "@ask", "@view", "", "@TASKGRAPH"})
	want := []string{"@browser", "@websearch", "mcp_search"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestGrantFromContext(t *testing.T) {
	ctx := context.WithValue(context.Background(), CtxKeyPluginGrant, &PluginGrant{Plugins: []string{"@browser", "@ask"}})
	got := grantFromContext(ctx)
	if len(got) != 1 || got[0] != "@browser" {
		t.Fatalf("denylisted @ask must be dropped: %v", got)
	}
	if grantFromContext(context.Background()) != nil {
		t.Fatal("no grant → nil")
	}
}

func TestPluginGrantForName(t *testing.T) {
	tools := []string{"@browser", "mcp_search"}
	for name, want := range map[string]string{
		"browser":    "@browser",
		"@browser":   "@browser",
		"mcp_search": "mcp_search",
		"forge":      "",
	} {
		got, ok := pluginGrantForName(tools, name)
		if (want == "" && ok) || (want != "" && got != want) {
			t.Fatalf("pluginGrantForName(%q) = %q,%v want %q", name, got, ok, want)
		}
	}
}

func TestPluginToolDefinitionsForRequiresRunner(t *testing.T) {
	RegisterPluginToolRunner(nil)
	RegisterPluginToolDefiner(func(names []string) []models.ToolDefinition {
		return []models.ToolDefinition{{Function: models.ToolFunctionDef{Name: "browser"}}}
	})
	t.Cleanup(func() { RegisterPluginToolDefiner(nil) })
	if PluginToolDefinitionsFor([]string{"@browser"}) != nil {
		t.Fatal("no runner → nil defs (a def without an executor guarantees a failing call)")
	}
	RegisterPluginToolRunner(func(context.Context, string, string) (string, error) { return "", nil })
	t.Cleanup(func() { RegisterPluginToolRunner(nil) })
	if len(PluginToolDefinitionsFor([]string{"@browser"})) != 1 {
		t.Fatal("runner+definer → defs")
	}
	if PluginToolDefinitionsFor(nil) != nil {
		t.Fatal("empty names → nil")
	}
}

func TestExecutePluginToolRoutesToRunner(t *testing.T) {
	var gotTool, gotArgs string
	RegisterPluginToolRunner(func(_ context.Context, tool, args string) (string, error) {
		gotTool, gotArgs = tool, args
		return "page loaded", nil
	})
	t.Cleanup(func() { RegisterPluginToolRunner(nil) })

	v := validatedTC{rtc: resolvedToolCall{
		ID: "1", pluginName: "@browser", Native: true,
		NativeArgs: map[string]interface{}{"cmd": "open", "args": map[string]interface{}{"url": "http://x"}},
	}}
	res := executePluginTool(context.Background(), v)
	if res.failed {
		t.Fatalf("unexpected failure: %s", res.output)
	}
	if gotTool != "@browser" || gotArgs == "" {
		t.Fatalf("runner got tool=%q args=%q", gotTool, gotArgs)
	}
}

func TestResolveToolCallsMarksGrantedPlugins(t *testing.T) {
	tools := []string{"@browser"}
	// Native call named after the def ("browser").
	resolved, _ := resolveToolCalls(true, []models.ToolCall{
		{ID: "1", Name: "browser", Arguments: map[string]interface{}{"cmd": "open"}},
	}, "", 0, tools)
	if len(resolved) != 1 || resolved[0].pluginName != "@browser" {
		t.Fatalf("native plugin not marked: %+v", resolved)
	}
	// Non-granted native call stays a normal (unmarked) call.
	resolved, _ = resolveToolCalls(true, []models.ToolCall{
		{ID: "2", Name: "read", Arguments: map[string]interface{}{}},
	}, "", 0, tools)
	if resolved[0].pluginName != "" {
		t.Fatal("non-granted call must not be marked as plugin")
	}
}

func TestPolicyCallSurfaceUsesRealPluginName(t *testing.T) {
	rtc := resolvedToolCall{
		pluginName: "@browser", Native: true,
		NativeArgs: map[string]interface{}{"cmd": "eval"},
	}
	name, args := policyCallSurface(rtc)
	if name != "@browser" {
		t.Fatalf("plugin call must surface under its real name, got %q", name)
	}
	if args == "" {
		t.Fatal("envelope args expected")
	}
	// A normal engine call still canonicalizes to @coder.
	name, _ = policyCallSurface(resolvedToolCall{Subcmd: "write", Native: true})
	if name != "@coder" {
		t.Fatalf("engine call must stay @coder, got %q", name)
	}
}
