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
	listed, statused                   bool
}

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
		"status": "status", "attached": "status", "info": "status",
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
