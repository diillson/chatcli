/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/diillson/chatcli/cli/agent/workers"
	"github.com/diillson/chatcli/cli/plugins"
	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

func newNativeBuiltinsAgent(t *testing.T) *AgentMode {
	t.Helper()
	mgr, _ := plugins.NewManager(zap.NewNop())
	for _, p := range []plugins.Plugin{
		plugins.NewBuiltinOsvPlugin(), plugins.NewBuiltinForgePlugin(), plugins.NewBuiltinWebSearchPlugin(),
		plugins.NewBuiltinReadPlugin(), plugins.NewBuiltinSearchPlugin(), plugins.NewBuiltinTreePlugin(),
		plugins.NewBuiltinParkPlugin(), plugins.NewBuiltinAskPlugin(), plugins.NewBuiltinWikipediaPlugin(),
	} {
		mgr.RegisterBuiltinPlugin(p)
	}
	return &AgentMode{cli: &ChatCLI{pluginManager: mgr}, logger: zap.NewNop()}
}

func TestBuiltinNativeDefs_EveryEligibleBuiltin(t *testing.T) {
	a := newNativeBuiltinsAgent(t)
	defs := a.builtinNativeDefs()
	names := map[string]models.ToolDefinition{}
	for _, d := range defs {
		names[d.Function.Name] = d
	}
	for _, want := range []string{"osv", "forge", "read", "search", "tree", "wikipedia"} {
		if _, ok := names[want]; !ok {
			t.Fatalf("builtin %s must get a native definition, got %v", want, keysOf(names))
		}
	}
	for _, skip := range []string{"websearch", "web_search", "park", "ask", "coder"} {
		if _, ok := names[skip]; ok {
			t.Fatalf("%s must not be synthesized (hand-mapped, denylisted or the engine itself)", skip)
		}
	}
	for n := range names {
		if _, coder := workers.NativeToolNameToSubcmd(n); coder {
			t.Fatalf("%s collides with a coder native tool", n)
		}
	}
	// The envelope: subcommand tools carry cmd+args, flat tools their flags.
	osv := names["osv"]
	raw, _ := json.Marshal(osv.Function.Parameters)
	if !strings.Contains(string(raw), `"cmd"`) || !strings.Contains(string(raw), `"scan"`) {
		t.Fatalf("osv definition must enumerate its subcommands: %s", raw)
	}
	read := names["read"]
	raw, _ = json.Marshal(read.Function.Parameters)
	if !strings.Contains(string(raw), `"file"`) {
		t.Fatalf("flat tool keeps its own flags: %s", raw)
	}
	// Sorted, so the tools array (and the cached prefix) is stable.
	for i := 1; i < len(defs); i++ {
		if defs[i-1].Function.Name > defs[i].Function.Name {
			t.Fatalf("definitions must be sorted: %s before %s", defs[i-1].Function.Name, defs[i].Function.Name)
		}
	}
}

func TestBuiltinNativeDefs_CommandScopeAndNilSafety(t *testing.T) {
	a := newNativeBuiltinsAgent(t)
	a.commandToolScope = []string{"osv"}
	defs := a.builtinNativeDefs()
	if len(defs) != 1 || defs[0].Function.Name != "osv" {
		t.Fatalf("command scope must filter the native set: %v", defs)
	}
	var none *AgentMode
	if none.builtinNativeDefs() != nil {
		t.Fatal("nil agent must yield nil")
	}
	if (&AgentMode{}).builtinNativeDefs() != nil {
		t.Fatal("agent without a cli must yield nil")
	}
}

func TestNativeBuiltinCall_RoutesEnvelope(t *testing.T) {
	a := newNativeBuiltinsAgent(t)
	tc, ok := a.nativeBuiltinCall(models.ToolCall{Name: "osv", Arguments: map[string]interface{}{"cmd": "scan", "args": map[string]interface{}{"path": "go.mod"}}})
	if !ok || tc.Name != "@osv" || !strings.Contains(tc.Args, `"cmd":"scan"`) || !strings.Contains(tc.Args, `"path":"go.mod"`) || tc.Raw != tc.Args {
		t.Fatalf("osv call = %+v %v", tc, ok)
	}
	if tc, ok := a.nativeBuiltinCall(models.ToolCall{Name: "@Wikipedia", Arguments: map[string]interface{}{"query": "Go"}}); !ok || tc.Name != "@wikipedia" || tc.Args != `{"query":"Go"}` {
		t.Fatalf("flat args and @-prefixed name: %+v %v", tc, ok)
	}
	if tc, ok := a.nativeBuiltinCall(models.ToolCall{Name: "tree"}); !ok || tc.Args != "{}" {
		t.Fatalf("missing arguments become an empty object: %+v %v", tc, ok)
	}
	for _, name := range []string{"read_file", "web_search", "websearch", "park", "ask", "coder", "nope", "find_tools"} {
		if _, ok := a.nativeBuiltinCall(models.ToolCall{Name: name}); ok {
			t.Fatalf("%s must not resolve as a synthesized builtin", name)
		}
	}
	if _, ok := (&AgentMode{}).nativeBuiltinCall(models.ToolCall{Name: "osv"}); ok {
		t.Fatal("agent without plugins must not resolve")
	}
}

func TestPrepareDeferredTools_BuiltinsTravelWithMCP(t *testing.T) {
	a := newNativeBuiltinsAgent(t)
	// No model is configured, so the defer threshold is the floor and the
	// builtins are advertised through the index, like MCP tools past it.
	idx := a.prepareDeferredTools()
	if len(a.deferrableTools) != len(a.builtinToolDefs) || len(a.builtinToolDefs) == 0 {
		t.Fatalf("builtins must be the deferrable set: %d/%d", len(a.deferrableTools), len(a.builtinToolDefs))
	}
	// Builtins that fit beside the core are activated from the start; the
	// rest are advertised in the index. Together they are the whole set.
	indexed := strings.Count(idx, "\n- ")
	if len(a.activatedTools) == 0 || indexed == 0 || len(a.activatedTools)+indexed != len(a.builtinToolDefs) || !strings.Contains(idx, "find_tools") {
		t.Fatalf("activated (%d) + indexed (%d) must cover %d builtins: %q", len(a.activatedTools), indexed, len(a.builtinToolDefs), idx)
	}
	for name := range a.activatedTools {
		if strings.Contains(idx, "- "+name+":") {
			t.Fatalf("%s travels and must not also be indexed", name)
		}
	}
	// Under a real threshold every builtin travels with the turn.
	plan := workers.PlanToolDefs(workers.PluginToolDefinitions(), a.deferrableTools, a.activatedTools, 1<<20)
	if plan.Deferred != 0 || len(plan.Defs) != len(workers.PluginToolDefinitions())+len(a.builtinToolDefs) {
		t.Fatalf("the turn must ship core plus every builtin: deferred=%d defs=%d", plan.Deferred, len(plan.Defs))
	}
	// find_tools answers with a deferred builtin's definition and activates it.
	out, handled := a.handleFindTools(models.ToolCall{Name: workers.FindToolsName, Arguments: map[string]interface{}{"query": "wikipedia"}})
	if !handled || !strings.Contains(out, "wikipedia") || !a.activatedTools["wikipedia"] {
		t.Fatalf("find_tools must surface and activate the builtin: %v %q", handled, out)
	}
}

func keysOf(m map[string]models.ToolDefinition) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
