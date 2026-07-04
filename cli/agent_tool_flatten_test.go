/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diillson/chatcli/cli/plugins"
)

// End-to-end regression for the reported bug: the agent flattens a {cmd,args}
// tool envelope into "--flag value" argv via parseToolArgsWithJSON, and the new
// builtins must parse that. This drives the REAL flattener into the REAL plugin.
func TestAgentFlatten_SkillCreateE2E(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)        // resolveSkillsDir → $HOME/.chatcli/skills (unix)
	t.Setenv("USERPROFILE", home) // windows

	argLine := `{"cmd":"create","args":{"name":"deploy-x","description":"How to deploy X","content":"# Deploy\nmake build","triggers":["deploy x","ship x"],"allowed_tools":["@coder","Bash"]}}`
	argv, err := parseToolArgsWithJSON(argLine)
	if err != nil {
		t.Fatalf("flatten: %v", err)
	}
	// Sanity: the flattener produced argv, not the JSON envelope.
	if len(argv) == 0 || argv[0] != "create" {
		t.Fatalf("unexpected flattened argv: %v", argv)
	}

	out, err := plugins.NewBuiltinSkillPlugin().Execute(context.Background(), argv)
	if err != nil {
		t.Fatalf("@skill create via flattened argv failed: %v\nargv=%v", err, argv)
	}
	_ = out

	path := filepath.Join(home, ".chatcli", "skills", "deploy-x", "SKILL.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("skill not written at %s: %v", path, err)
	}
	s := string(data)
	for _, want := range []string{`name: "deploy-x"`, `- "deploy x"`, `- "ship x"`, `allowed-tools: ["@coder","Bash"]`, "# Deploy"} {
		if !strings.Contains(s, want) {
			t.Errorf("SKILL.md missing %q\n---\n%s", want, s)
		}
	}
}

// Same chain for @session via its adapter (proves --query is parsed, not taken
// literally as "--query ...").
type flattenFakeSession struct{ q string }

func (f *flattenFakeSession) Search(_ context.Context, query string, _ int) (string, error) {
	f.q = query
	return "ok", nil
}
func (f *flattenFakeSession) List(context.Context) (string, error) { return "", nil }

// Same chain for @tools describe — the exact shapes a model emits in agent
// mode. The flattener turns the {cmd,args} envelope into "describe --name X"
// argv, and the plugin's argv path must parse the flag instead of taking
// "--name" literally as the tool name.
type flattenFakeCatalog struct{ described string }

func (f *flattenFakeCatalog) Describe(name string) (string, error) {
	f.described = name
	return "DEF " + name, nil
}
func (f *flattenFakeCatalog) List() (string, error) { return "INDEX", nil }

func TestAgentFlatten_ToolsDescribeE2E(t *testing.T) {
	cases := []struct {
		argLine string
		want    string
	}{
		// Canonical envelope — the shape the system prompt itself teaches.
		{`{"cmd":"describe","args":{"name":"@graphview"}}`, "@graphview"},
		// Flat variant models fall back to: name at the top level.
		{`{"cmd":"describe","name":"graphview"}`, "graphview"},
		// Raw argv string with a flag.
		{`describe --name graphview`, "graphview"},
		// Raw argv string, positional.
		{`describe @graphview`, "@graphview"},
	}
	for _, tc := range cases {
		f := &flattenFakeCatalog{}
		plugins.SetToolCatalogAdapter(f)
		t.Cleanup(func() { plugins.SetToolCatalogAdapter(nil) })

		argv, err := parseToolArgsWithJSON(tc.argLine)
		if err != nil {
			t.Fatalf("%s: flatten: %v", tc.argLine, err)
		}
		if _, err := plugins.NewBuiltinToolsPlugin().Execute(context.Background(), argv); err != nil {
			t.Fatalf("%s: @tools via flattened argv failed: %v\nargv=%v", tc.argLine, err, argv)
		}
		if f.described != tc.want {
			t.Errorf("%s: described %q, want %q (argv=%v)", tc.argLine, f.described, tc.want, argv)
		}
	}
}

// flattenExec drives the REAL flattener into the given plugin — the exact
// path agent mode takes for a <tool_call> envelope.
func flattenExec(t *testing.T, p plugins.Plugin, argLine string) (string, error) {
	t.Helper()
	argv, err := parseToolArgsWithJSON(argLine)
	if err != nil {
		t.Fatalf("%s: flatten: %v", argLine, err)
	}
	return p.Execute(context.Background(), argv)
}

type flattenFakeLSP struct {
	file         string
	line, column int
}

func (f *flattenFakeLSP) Diagnostics(file string) (string, error) { f.file = file; return "ok", nil }
func (f *flattenFakeLSP) Definition(file string, line, column int) (string, error) {
	f.file, f.line, f.column = file, line, column
	return "ok", nil
}
func (f *flattenFakeLSP) References(file string, line, column int, _ bool, _ int) (string, error) {
	f.file, f.line, f.column = file, line, column
	return "ok", nil
}
func (f *flattenFakeLSP) Symbols(file string) (string, error) { f.file = file; return "ok", nil }
func (f *flattenFakeLSP) Hover(file string, line, column int) (string, error) {
	f.file, f.line, f.column = file, line, column
	return "ok", nil
}

func TestAgentFlatten_LSPDefinitionE2E(t *testing.T) {
	f := &flattenFakeLSP{}
	plugins.SetLSPAdapter(f)
	t.Cleanup(func() { plugins.SetLSPAdapter(nil) })

	if _, err := flattenExec(t, plugins.NewBuiltinLSPPlugin(),
		`{"cmd":"definition","args":{"file":"cli/cli.go","line":128,"column":14}}`); err != nil {
		t.Fatalf("@lsp definition via flattened argv failed: %v", err)
	}
	if f.file != "cli/cli.go" || f.line != 128 || f.column != 14 {
		t.Fatalf("definition got (%q,%d,%d), want (cli/cli.go,128,14)", f.file, f.line, f.column)
	}

	if _, err := flattenExec(t, plugins.NewBuiltinLSPPlugin(),
		`{"cmd":"diagnostics","args":{"file":"cli/agent_mode.go"}}`); err != nil {
		t.Fatalf("@lsp diagnostics via flattened argv failed: %v", err)
	}
	if f.file != "cli/agent_mode.go" {
		t.Fatalf("diagnostics file = %q", f.file)
	}
}

type flattenFakeProc struct {
	command, dir, id string
	tail             int
}

func (f *flattenFakeProc) Start(command, dir string) (string, error) {
	f.command, f.dir = command, dir
	return "p1", nil
}
func (f *flattenFakeProc) Status(id string) (string, error) { f.id = id; return "ok", nil }
func (f *flattenFakeProc) Logs(id string, tail int) (string, error) {
	f.id, f.tail = id, tail
	return "ok", nil
}
func (f *flattenFakeProc) Stop(id string) (string, error)   { f.id = id; return "ok", nil }
func (f *flattenFakeProc) Remove(id string) (string, error) { f.id = id; return "ok", nil }
func (f *flattenFakeProc) List() (string, error)            { return "ok", nil }

func TestAgentFlatten_ProcE2E(t *testing.T) {
	f := &flattenFakeProc{}
	plugins.SetProcAdapter(f)
	t.Cleanup(func() { plugins.SetProcAdapter(nil) })

	if _, err := flattenExec(t, plugins.NewBuiltinProcPlugin(),
		`{"cmd":"start","args":{"command":"npm run dev","dir":"./web"}}`); err != nil {
		t.Fatalf("@proc start via flattened argv failed: %v", err)
	}
	if f.command != "npm run dev" || f.dir != "./web" {
		t.Fatalf("start got (%q,%q), want (npm run dev,./web)", f.command, f.dir)
	}

	if _, err := flattenExec(t, plugins.NewBuiltinProcPlugin(),
		`{"cmd":"status","args":{"id":"p1"}}`); err != nil {
		t.Fatalf("@proc status via flattened argv failed: %v", err)
	}
	if f.id != "p1" {
		t.Fatalf("status id = %q, want p1", f.id)
	}

	if _, err := flattenExec(t, plugins.NewBuiltinProcPlugin(),
		`{"cmd":"logs","args":{"id":"p2","tail":50}}`); err != nil {
		t.Fatalf("@proc logs via flattened argv failed: %v", err)
	}
	if f.id != "p2" || f.tail != 50 {
		t.Fatalf("logs got (%q,%d), want (p2,50)", f.id, f.tail)
	}
}

type flattenFakeTodo struct {
	items  []plugins.TodoItem
	id     int
	status string
}

func (f *flattenFakeTodo) Write(items []plugins.TodoItem) (string, error) {
	f.items = items
	return "ok", nil
}
func (f *flattenFakeTodo) List() (string, error) { return "ok", nil }
func (f *flattenFakeTodo) Mark(id int, status, _ string) (string, error) {
	f.id, f.status = id, status
	return "ok", nil
}

func TestAgentFlatten_TodoE2E(t *testing.T) {
	f := &flattenFakeTodo{}
	plugins.SetTodoAdapter(f)
	t.Cleanup(func() { plugins.SetTodoAdapter(nil) })

	if _, err := flattenExec(t, plugins.NewBuiltinTodoPlugin(),
		`{"cmd":"write","args":{"todos":[{"description":"map the code"},{"description":"apply fix","status":"in_progress"}]}}`); err != nil {
		t.Fatalf("@todo write via flattened argv failed: %v", err)
	}
	if len(f.items) != 2 || f.items[0].Description != "map the code" || f.items[1].Status != "in_progress" {
		t.Fatalf("write items = %+v", f.items)
	}

	if _, err := flattenExec(t, plugins.NewBuiltinTodoPlugin(),
		`{"cmd":"mark","args":{"id":2,"status":"completed"}}`); err != nil {
		t.Fatalf("@todo mark via flattened argv failed: %v", err)
	}
	if f.id != 2 || f.status != "completed" {
		t.Fatalf("mark got (%d,%q), want (2,completed)", f.id, f.status)
	}
}

type flattenFakeKnowledge struct {
	query, kb string
	topK      int
}

func (f *flattenFakeKnowledge) Search(query, kb string, topK int) (string, error) {
	f.query, f.kb, f.topK = query, kb, topK
	return "ok", nil
}
func (f *flattenFakeKnowledge) Get(string, string, int) (string, error) { return "ok", nil }
func (f *flattenFakeKnowledge) TOC(string, string) (string, error)      { return "ok", nil }
func (f *flattenFakeKnowledge) List() (string, error)                   { return "ok", nil }

func TestAgentFlatten_KnowledgeSearchE2E(t *testing.T) {
	f := &flattenFakeKnowledge{}
	plugins.SetKnowledgeAdapter(f)
	t.Cleanup(func() { plugins.SetKnowledgeAdapter(nil) })

	if _, err := flattenExec(t, plugins.NewBuiltinKnowledgePlugin(),
		`{"cmd":"search","args":{"query":"gateway voice env vars","top_k":10}}`); err != nil {
		t.Fatalf("@knowledge search via flattened argv failed: %v", err)
	}
	if f.query != "gateway voice env vars" || f.topK != 10 {
		t.Fatalf("search got (%q,%d), want (gateway voice env vars,10)", f.query, f.topK)
	}
}

type flattenFakeCompress struct {
	statsCalled bool
	content     string
}

func (f *flattenFakeCompress) Recall(string) (string, bool) { return "", false }
func (f *flattenFakeCompress) Compress(_, content string) (string, error) {
	f.content = content
	return "ok", nil
}
func (f *flattenFakeCompress) Stats() string { f.statsCalled = true; return "stats-ok" }

func TestAgentFlatten_CompressStatsE2E(t *testing.T) {
	f := &flattenFakeCompress{}
	plugins.SetCompressionAdapter(f)
	t.Cleanup(func() { plugins.SetCompressionAdapter(nil) })

	out, err := flattenExec(t, plugins.NewBuiltinCompressPlugin(), `{"cmd":"stats"}`)
	if err != nil {
		t.Fatalf("@compress stats via flattened argv failed: %v", err)
	}
	if !f.statsCalled || out != "stats-ok" {
		t.Fatalf("stats not dispatched: out=%q statsCalled=%v content=%q", out, f.statsCalled, f.content)
	}
}

type flattenFakeMemory struct{ updates map[string]string }

func (f *flattenFakeMemory) Remember(string, string) (string, error) { return "ok", nil }
func (f *flattenFakeMemory) UpdateProfile(updates map[string]string) (string, error) {
	f.updates = updates
	return "ok", nil
}
func (f *flattenFakeMemory) Forget(string) (string, error) { return "ok", nil }
func (f *flattenFakeMemory) Recall(string) (string, error) { return "ok", nil }

func TestAgentFlatten_MemoryProfileE2E(t *testing.T) {
	f := &flattenFakeMemory{}
	plugins.SetMemoryAdapter(f)
	t.Cleanup(func() { plugins.SetMemoryAdapter(nil) })

	if _, err := flattenExec(t, plugins.NewBuiltinMemoryPlugin(),
		`{"cmd":"profile","args":{"fields":{"role":"SRE","team":"infra"}}}`); err != nil {
		t.Fatalf("@memory profile via flattened argv failed: %v", err)
	}
	if f.updates["role"] != "SRE" || f.updates["team"] != "infra" {
		t.Fatalf("profile updates = %+v", f.updates)
	}
}

type flattenFakeContext struct {
	mergeName    string
	mergeSources []string
}

func (f *flattenFakeContext) Create(string, string, []string, string, bool) (string, error) {
	return "ok", nil
}
func (f *flattenFakeContext) Update(string, []string, string, string, []string) (string, error) {
	return "ok", nil
}
func (f *flattenFakeContext) Attach(string, int, int) (string, error) { return "ok", nil }
func (f *flattenFakeContext) Detach(string) (string, error)           { return "ok", nil }
func (f *flattenFakeContext) List() (string, error)                   { return "ok", nil }
func (f *flattenFakeContext) Show(string) (string, error)             { return "ok", nil }
func (f *flattenFakeContext) Inspect(string, int) (string, error)     { return "ok", nil }
func (f *flattenFakeContext) Merge(name string, sources []string, _ string) (string, error) {
	f.mergeName, f.mergeSources = name, sources
	return "ok", nil
}
func (f *flattenFakeContext) Status() (string, error)               { return "ok", nil }
func (f *flattenFakeContext) Export(string, string) (string, error) { return "ok", nil }
func (f *flattenFakeContext) Import(string) (string, error)         { return "ok", nil }
func (f *flattenFakeContext) Metrics() (string, error)              { return "ok", nil }
func (f *flattenFakeContext) Delete(string) (string, error)         { return "ok", nil }

func TestAgentFlatten_ContextMergeE2E(t *testing.T) {
	f := &flattenFakeContext{}
	plugins.SetContextAdapter(f)
	t.Cleanup(func() { plugins.SetContextAdapter(nil) })

	if _, err := flattenExec(t, plugins.NewBuiltinContextPlugin(),
		`{"cmd":"merge","args":{"name":"all-docs","sources":["react-docs","next-docs"]}}`); err != nil {
		t.Fatalf("@context merge via flattened argv failed: %v", err)
	}
	if f.mergeName != "all-docs" || len(f.mergeSources) != 2 ||
		f.mergeSources[0] != "react-docs" || f.mergeSources[1] != "next-docs" {
		t.Fatalf("merge got (%q,%v)", f.mergeName, f.mergeSources)
	}
}

// Top-level keys outside the {cmd,args} envelope must reach the plugin as
// flags, not be silently dropped by the flattener.
func TestFlatten_TopLevelKeysNotDropped(t *testing.T) {
	argv, err := parseToolArgsWithJSON(`{"cmd":"describe","name":"graphview"}`)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"describe", "--name", "graphview"}
	if len(argv) != len(want) {
		t.Fatalf("argv = %v, want %v", argv, want)
	}
	for i := range want {
		if argv[i] != want[i] {
			t.Fatalf("argv = %v, want %v", argv, want)
		}
	}
}

func TestAgentFlatten_SessionSearchE2E(t *testing.T) {
	f := &flattenFakeSession{}
	plugins.SetSessionAdapter(f)
	t.Cleanup(func() { plugins.SetSessionAdapter(nil) })

	argv, err := parseToolArgsWithJSON(`{"cmd":"search","args":{"query":"chapolin colorado"}}`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plugins.NewBuiltinSessionPlugin().Execute(context.Background(), argv); err != nil {
		t.Fatal(err)
	}
	if f.q != "chapolin colorado" {
		t.Fatalf("query = %q (flatten/parse regression)", f.q)
	}
}
