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

// fakeContextAdapter records calls for assertions.
type fakeContextAdapter struct {
	createName, createMode, createDesc string
	createPaths                        []string
	createForce                        bool
	attachName                         string
	attachRag, attachPriority          int
	detachName, deleteName             string
	listed, statused, metricsed        bool
	updateName, showName, mergeName    string
	inspectName                        string
	inspectChunk                       int
	mergeSources                       []string
	exportName, exportPath, importPath string
}

func (f *fakeContextAdapter) Update(name string, paths []string, mode, desc string, tags []string) (string, error) {
	f.updateName = name
	return "updated " + name, nil
}
func (f *fakeContextAdapter) Show(name string) (string, error) {
	f.showName = name
	return "show " + name, nil
}
func (f *fakeContextAdapter) Inspect(name string, chunk int) (string, error) {
	f.inspectName, f.inspectChunk = name, chunk
	return "inspect " + name, nil
}
func (f *fakeContextAdapter) Merge(name string, sources []string, desc string) (string, error) {
	f.mergeName, f.mergeSources = name, sources
	return "merged " + name, nil
}
func (f *fakeContextAdapter) Export(name, path string) (string, error) {
	f.exportName, f.exportPath = name, path
	return "exported " + name, nil
}
func (f *fakeContextAdapter) Import(path string) (string, error) {
	f.importPath = path
	return "imported", nil
}
func (f *fakeContextAdapter) Metrics() (string, error) { f.metricsed = true; return "metrics", nil }

func (f *fakeContextAdapter) Create(name, mode string, paths []string, desc string, force bool) (string, error) {
	f.createName, f.createMode, f.createPaths, f.createDesc, f.createForce = name, mode, paths, desc, force
	return "created " + name, nil
}
func (f *fakeContextAdapter) Attach(name string, ragTopK, priority int) (string, error) {
	f.attachName, f.attachRag, f.attachPriority = name, ragTopK, priority
	return "attached " + name, nil
}
func (f *fakeContextAdapter) Detach(name string) (string, error) {
	f.detachName = name
	return "detached " + name, nil
}
func (f *fakeContextAdapter) List() (string, error)   { f.listed = true; return "list", nil }
func (f *fakeContextAdapter) Status() (string, error) { f.statused = true; return "status", nil }
func (f *fakeContextAdapter) Delete(name string) (string, error) {
	f.deleteName = name
	return "deleted " + name, nil
}

func withFakeContextAdapter(t *testing.T) *fakeContextAdapter {
	t.Helper()
	f := &fakeContextAdapter{}
	SetContextAdapter(f)
	t.Cleanup(func() { SetContextAdapter(nil) })
	return f
}

func TestCanonicalContextCmd(t *testing.T) {
	cases := map[string]string{
		"create": "create", "new": "create",
		"attach": "attach", "add": "attach",
		"detach": "detach", "remove": "detach", "rm": "detach",
		"list": "list", "ls": "list",
		"status": "status", "attached": "status",
		"update": "update", "edit": "update",
		"show": "show", "info": "show", "view": "show",
		"inspect": "inspect",
		"merge":   "merge", "join": "merge",
		"export": "export", "import": "import",
		"metrics": "metrics", "stats": "metrics",
		"delete": "delete", "del": "delete",
		"bogus": "",
	}
	for in, want := range cases {
		if got := canonicalContextCmd(in); got != want {
			t.Errorf("canonicalContextCmd(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestContextRagTopK(t *testing.T) {
	parse := func(payload string) int {
		_, raw, err := parseContextInvocation([]string{payload})
		if err != nil {
			t.Fatalf("parse %s: %v", payload, err)
		}
		return contextRagTopK(raw)
	}
	if got := parse(`{"cmd":"attach","args":{"name":"x","rag":true}}`); got != contextDefaultRagTopK {
		t.Errorf("rag:true => %d, want %d", got, contextDefaultRagTopK)
	}
	if got := parse(`{"cmd":"attach","args":{"name":"x","rag":12}}`); got != 12 {
		t.Errorf("rag:12 => %d, want 12", got)
	}
	if got := parse(`{"cmd":"attach","args":{"name":"x","rag":false}}`); got != 0 {
		t.Errorf("rag:false => %d, want 0", got)
	}
	if got := parse(`{"cmd":"attach","args":{"name":"x"}}`); got != 0 {
		t.Errorf("absent rag => %d, want 0", got)
	}
}

func TestContextStringSlice(t *testing.T) {
	_, raw, err := parseContextInvocation([]string{`{"cmd":"create","args":{"name":"x","paths":["a.jsonl","b"]}}`})
	if err != nil {
		t.Fatal(err)
	}
	got := contextStringSlice(raw, "paths", "path")
	if strings.Join(got, ",") != "a.jsonl,b" {
		t.Fatalf("array paths = %v", got)
	}
	// CSV single string
	_, raw2, _ := parseContextInvocation([]string{`{"cmd":"create","args":{"name":"x","paths":"a.jsonl, b"}}`})
	got2 := contextStringSlice(raw2, "paths")
	if strings.Join(got2, ",") != "a.jsonl,b" {
		t.Fatalf("csv paths = %v", got2)
	}
}

func TestContextExecuteDispatch(t *testing.T) {
	f := withFakeContextAdapter(t)
	p := NewBuiltinContextPlugin()
	run := func(payload string) (string, error) {
		return p.Execute(context.Background(), []string{payload})
	}

	if _, err := run(`{"cmd":"create","args":{"name":"react","paths":["/tmp/r.jsonl"],"mode":"knowledge","force":true}}`); err != nil {
		t.Fatal(err)
	}
	if f.createName != "react" || f.createMode != "knowledge" || !f.createForce || strings.Join(f.createPaths, ",") != "/tmp/r.jsonl" {
		t.Fatalf("create routed wrong: %+v", f)
	}

	if _, err := run(`{"cmd":"attach","args":{"name":"react","rag":8,"priority":5}}`); err != nil {
		t.Fatal(err)
	}
	if f.attachName != "react" || f.attachRag != 8 || f.attachPriority != 5 {
		t.Fatalf("attach routed wrong: %+v", f)
	}

	if _, err := run(`{"cmd":"detach","args":{"name":"react"}}`); err != nil || f.detachName != "react" {
		t.Fatalf("detach routed wrong: %v %q", err, f.detachName)
	}
	if _, err := run(`{"cmd":"delete","args":{"name":"react"}}`); err != nil || f.deleteName != "react" {
		t.Fatalf("delete routed wrong: %v %q", err, f.deleteName)
	}
	if _, err := run(`{"cmd":"list"}`); err != nil || !f.listed {
		t.Fatalf("list routed wrong: %v", err)
	}
	if _, err := run(`{"cmd":"status"}`); err != nil || !f.statused {
		t.Fatalf("status routed wrong: %v", err)
	}
}

func TestContextExecuteDispatch_FullParity(t *testing.T) {
	f := withFakeContextAdapter(t)
	p := NewBuiltinContextPlugin()
	run := func(payload string) (string, error) {
		return p.Execute(context.Background(), []string{payload})
	}

	if _, err := run(`{"cmd":"update","args":{"name":"react","paths":["/tmp/v19.jsonl"]}}`); err != nil || f.updateName != "react" {
		t.Fatalf("update: %v %q", err, f.updateName)
	}
	if _, err := run(`{"cmd":"show","args":{"name":"react"}}`); err != nil || f.showName != "react" {
		t.Fatalf("show: %v %q", err, f.showName)
	}
	if _, err := run(`{"cmd":"inspect","args":{"name":"react","chunk":3}}`); err != nil || f.inspectName != "react" || f.inspectChunk != 3 {
		t.Fatalf("inspect: %v %q %d", err, f.inspectName, f.inspectChunk)
	}
	if _, err := run(`{"cmd":"merge","args":{"name":"all","sources":["a","b"]}}`); err != nil || f.mergeName != "all" || len(f.mergeSources) != 2 {
		t.Fatalf("merge: %v %+v", err, f)
	}
	if _, err := run(`{"cmd":"export","args":{"name":"react","path":"/tmp/r.json"}}`); err != nil || f.exportName != "react" || f.exportPath != "/tmp/r.json" {
		t.Fatalf("export: %v %+v", err, f)
	}
	if _, err := run(`{"cmd":"import","args":{"path":"/tmp/r.json"}}`); err != nil || f.importPath != "/tmp/r.json" {
		t.Fatalf("import: %v %q", err, f.importPath)
	}
	if _, err := run(`{"cmd":"metrics"}`); err != nil || !f.metricsed {
		t.Fatalf("metrics: %v", err)
	}
	// aliases
	if _, err := run(`{"cmd":"info","args":{"name":"react"}}`); err != nil || f.showName != "react" {
		t.Fatalf("info alias should route to show: %v", err)
	}
}

func TestContextExecuteValidation_FullParity(t *testing.T) {
	withFakeContextAdapter(t)
	p := NewBuiltinContextPlugin()
	bad := []string{
		`{"cmd":"update","args":{}}`,                             // no name
		`{"cmd":"show","args":{}}`,                               // no name
		`{"cmd":"merge","args":{"name":"x","sources":["only"]}}`, // < 2 sources
		`{"cmd":"export","args":{"name":"x"}}`,                   // no path
		`{"cmd":"import","args":{}}`,                             // no path
	}
	for _, payload := range bad {
		if _, err := p.Execute(context.Background(), []string{payload}); err == nil {
			t.Errorf("expected validation error for %s", payload)
		}
	}
}

func TestContextExecuteValidation(t *testing.T) {
	withFakeContextAdapter(t)
	p := NewBuiltinContextPlugin()
	// create without name
	if _, err := p.Execute(context.Background(), []string{`{"cmd":"create","args":{"paths":["x"]}}`}); err == nil {
		t.Error("expected error for create without name")
	}
	// create without paths
	if _, err := p.Execute(context.Background(), []string{`{"cmd":"create","args":{"name":"x"}}`}); err == nil {
		t.Error("expected error for create without paths")
	}
	// attach without name
	if _, err := p.Execute(context.Background(), []string{`{"cmd":"attach","args":{}}`}); err == nil {
		t.Error("expected error for attach without name")
	}
	// unknown cmd
	if _, err := p.Execute(context.Background(), []string{`{"cmd":"frobnicate"}`}); err == nil {
		t.Error("expected error for unknown cmd")
	}
}

func TestContextNoAdapter(t *testing.T) {
	SetContextAdapter(nil)
	p := NewBuiltinContextPlugin()
	if _, err := p.Execute(context.Background(), []string{`{"cmd":"list"}`}); err == nil {
		t.Error("expected error when no adapter is wired")
	}
}

func TestContextCapsReadOnly(t *testing.T) {
	p := NewBuiltinContextPlugin()
	readonly := []string{`{"cmd":"list"}`, `{"cmd":"status"}`}
	for _, payload := range readonly {
		if !p.IsReadOnly([]string{payload}) {
			t.Errorf("%s should be read-only", payload)
		}
	}
	mutating := []string{
		`{"cmd":"create","args":{"name":"x","paths":["y"]}}`,
		`{"cmd":"attach","args":{"name":"x"}}`,
		`{"cmd":"detach","args":{"name":"x"}}`,
		`{"cmd":"delete","args":{"name":"x"}}`,
	}
	for _, payload := range mutating {
		if p.IsReadOnly([]string{payload}) {
			t.Errorf("%s should NOT be read-only", payload)
		}
	}
}

func TestContextArgvForm(t *testing.T) {
	cmd, raw, err := parseContextInvocation([]string{"attach", "react-docs", "--rag", "8"})
	if err != nil {
		t.Fatal(err)
	}
	if cmd != "attach" {
		t.Fatalf("cmd = %q", cmd)
	}
	if jsonString(raw, "name") != "react-docs" {
		t.Fatalf("name = %q", jsonString(raw, "name"))
	}
	if jsonInt(raw, "rag") != 8 {
		t.Fatalf("rag = %d", jsonInt(raw, "rag"))
	}
}
