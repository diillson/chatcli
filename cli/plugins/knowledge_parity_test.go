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

// Every call below is the argv the agent loop hands a plugin after
// flattening {"cmd":X,"args":{...}}: [X, "--key", "value", ...], keys sorted.

type fakeMemoryInspector struct {
	fakeFactBrowser
}

func (f *fakeMemoryInspector) Stats() (string, error)         { return f.ListFacts("stats-out", "", 0) }
func (f *fakeMemoryInspector) WhyLastRecall() (string, error) { return f.ListFacts("why-out", "", 0) }

func TestMemoryTool_StatsAndWhy(t *testing.T) {
	fake := &fakeMemoryInspector{}
	SetMemoryAdapter(fake)
	t.Cleanup(func() { SetMemoryAdapter(nil) })
	p := NewBuiltinMemoryPlugin()
	ctx := context.Background()

	for _, cmd := range []string{"stats", "why"} {
		// The fake records which method ran as a list call carrying its name.
		if _, err := p.Execute(ctx, []string{cmd}); err != nil || fake.calls[len(fake.calls)-1] != "list:"+cmd+"-out::0" {
			t.Fatalf("%s: %v, calls %v", cmd, err, fake.calls)
		}
		if !p.IsReadOnly([]string{cmd}) {
			t.Errorf("%s only reads and must not ask for confirmation", cmd)
		}
		if !strings.Contains(p.Usage(), "\n  "+cmd+" ") || !strings.Contains(p.Schema(), `"name":"`+cmd+`"`) {
			t.Errorf("%s is missing from Usage or Schema", cmd)
		}
	}

	before := len(fake.calls)
	if _, err := p.Execute(ctx, []string{"stats", "--category", "gotcha"}); err == nil || !strings.Contains(err.Error(), "category") {
		t.Fatalf("an argument stats does not take must be reported, got %v", err)
	}
	if len(fake.calls) != before {
		t.Fatal("a refused call must not reach the adapter")
	}

	SetMemoryAdapter(&fakeFactBrowser{})
	if _, err := p.Execute(ctx, []string{"why"}); err == nil {
		t.Fatal("an adapter without the inspector capability must report it")
	}
}

type fakeSkillSession struct{ calls []string }

func (f *fakeSkillSession) note(call string) (string, error) {
	f.calls = append(f.calls, call)
	return call, nil
}
func (f *fakeSkillSession) Pin(name string) (string, error)   { return f.note("pin:" + name) }
func (f *fakeSkillSession) Unpin(name string) (string, error) { return f.note("unpin:" + name) }
func (f *fakeSkillSession) RegistrySearch(_ context.Context, query string) (string, error) {
	return f.note("search:" + query)
}
func (f *fakeSkillSession) RegistryInfo(_ context.Context, name, from string) (string, error) {
	return f.note("info:" + name + ":" + from)
}

func TestSkillTool_SessionVerbs(t *testing.T) {
	fake := &fakeSkillSession{}
	SetSkillAdapter(fake)
	t.Cleanup(func() { SetSkillAdapter(nil) })
	skillsDirOverride = t.TempDir()
	t.Cleanup(func() { skillsDirOverride = "" })
	p := NewBuiltinSkillPlugin()
	ctx := context.Background()

	cases := []struct {
		argv     []string
		call     string
		readOnly bool
	}{
		{[]string{"pin", "--name", "deploy-x"}, "pin:deploy-x", false},
		{[]string{"unpin", "--name", "deploy-x"}, "unpin:deploy-x", false},
		{[]string{"search", "--query", "frontend design"}, "search:frontend design", true},
		{[]string{"info", "--name", "fd"}, "info:fd:", true},
		{[]string{"info", "--from", "skills.sh", "--name", "fd"}, "info:fd:skills.sh", true},
	}
	for _, tc := range cases {
		out, err := p.Execute(ctx, tc.argv)
		if err != nil || out != tc.call {
			t.Fatalf("%v = %q, %v; calls %v", tc.argv, out, err, fake.calls)
		}
		if p.IsReadOnly(tc.argv) != tc.readOnly {
			t.Errorf("%v: IsReadOnly = %v, want %v", tc.argv, !tc.readOnly, tc.readOnly)
		}
		if d := p.DescribeCall(tc.argv); d == p.DescribeCall([]string{"list"}) {
			t.Errorf("%v is described as a listing: %q", tc.argv, d)
		}
		if !strings.Contains(p.Schema(), `"name":"`+tc.argv[0]+`"`) {
			t.Errorf("%s is missing from Schema", tc.argv[0])
		}
	}

	before := len(fake.calls)
	for _, argv := range [][]string{
		{"info", "--frm", "skills.sh", "--name", "fd"}, // mistyped flag
		{"pin"},                       // name missing
		{"install", "--name", "fd"},   // alias of import: needs a pack path
		{"uninstall", "--name", "fd"}, // not a subcommand of the tool
	} {
		if out, err := p.Execute(ctx, argv); err == nil {
			t.Errorf("%v must be reported, got %q", argv, out)
		}
	}
	if len(fake.calls) != before {
		t.Fatalf("refused calls reached the adapter: %v", fake.calls[before:])
	}
	if !strings.Contains(p.Usage(), "NOT available") {
		t.Error("Usage must say registry install is not available to the tool")
	}

	SetSkillAdapter(nil)
	if _, err := p.Execute(ctx, []string{"pin", "--name", "deploy-x"}); err == nil {
		t.Fatal("without a session adapter pin must report it is unavailable")
	}
}

type fakeSessionStatus struct {
	fakeSessionAdapter
	asked int
}

func (f *fakeSessionStatus) Status(context.Context) (string, error) {
	f.asked++
	return "bound to alpha", nil
}

func TestSessionTool_Status(t *testing.T) {
	fake := &fakeSessionStatus{}
	withSessionAdapter(t, fake)
	p := NewBuiltinSessionPlugin()
	ctx := context.Background()

	if out, err := p.Execute(ctx, []string{"status"}); err != nil || out != "bound to alpha" {
		t.Fatalf("status = %q, %v", out, err)
	}
	if !p.IsReadOnly([]string{"status"}) {
		t.Error("status only reads and must not ask for confirmation")
	}
	if d := p.DescribeCall([]string{"status"}); d == p.DescribeCall([]string{"search", "--query", ""}) {
		t.Errorf("status is described as a search: %q", d)
	}
	if !strings.Contains(p.Usage(), "\n  status ") || !strings.Contains(p.Schema(), `"name":"status"`) {
		t.Error("status is missing from Usage or Schema")
	}
	if _, err := p.Execute(ctx, []string{"status", "--name", "beta"}); err == nil || fake.asked != 1 {
		t.Fatalf("an argument status does not take must be reported (err %v, asked %d)", err, fake.asked)
	}

	withSessionAdapter(t, &fakeSessionAdapter{})
	if _, err := p.Execute(ctx, []string{"status"}); err == nil {
		t.Fatal("an adapter without the status capability must report it")
	}
}
