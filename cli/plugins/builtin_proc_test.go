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

type fakeProcAdapter struct {
	pattern  string
	timeout  int
	lastCall string
	command  string
	dir      string
	id       string
	tail     int
}

func (f *fakeProcAdapter) Start(command, dir string) (string, error) {
	f.lastCall, f.command, f.dir = "start", command, dir
	return "started p1", nil
}
func (f *fakeProcAdapter) Status(id string) (string, error) {
	f.lastCall, f.id = "status", id
	return "status", nil
}
func (f *fakeProcAdapter) Logs(id string, tail int) (string, error) {
	f.lastCall, f.id, f.tail = "logs", id, tail
	return "logs", nil
}
func (f *fakeProcAdapter) Stop(id string) (string, error) {
	f.lastCall, f.id = "stop", id
	return "stopped", nil
}
func (f *fakeProcAdapter) Remove(id string) (string, error) {
	f.lastCall, f.id = "remove", id
	return "removed", nil
}
func (f *fakeProcAdapter) List() (string, error) {
	f.lastCall = "list"
	return "list", nil
}
func (f *fakeProcAdapter) Wait(_ context.Context, id, pattern string, timeoutSec int) (string, error) {
	f.lastCall, f.id, f.pattern, f.timeout = "wait", id, pattern, timeoutSec
	return "waited", nil
}
func (f *fakeProcAdapter) LogsGrep(id, pattern string, tail int) (string, error) {
	f.lastCall, f.id, f.pattern, f.tail = "grep", id, pattern, tail
	return "grepped", nil
}

func withFakeProc(t *testing.T) *fakeProcAdapter {
	t.Helper()
	fake := &fakeProcAdapter{}
	SetProcAdapter(fake)
	t.Cleanup(func() { SetProcAdapter(nil) })
	return fake
}

func TestProcEnvelopeDispatch(t *testing.T) {
	fake := withFakeProc(t)
	p := NewBuiltinProcPlugin()

	if _, err := p.Execute(context.Background(),
		[]string{`{"cmd":"start","args":{"command":"npm run dev","dir":"./web"}}`}); err != nil {
		t.Fatalf("start: %v", err)
	}
	if fake.lastCall != "start" || fake.command != "npm run dev" || fake.dir != "./web" {
		t.Fatalf("start args not threaded: %+v", fake)
	}

	if _, err := p.Execute(context.Background(),
		[]string{`{"cmd":"logs","args":{"id":"p1","tail":50}}`}); err != nil {
		t.Fatalf("logs: %v", err)
	}
	if fake.lastCall != "logs" || fake.id != "p1" || fake.tail != 50 {
		t.Fatalf("logs args not threaded: %+v", fake)
	}
}

func TestProcArgvFormsAndAliases(t *testing.T) {
	fake := withFakeProc(t)
	p := NewBuiltinProcPlugin()

	// argv start joins the remaining args into the command.
	if _, err := p.Execute(context.Background(), []string{"run", "go", "run", "./cmd/server"}); err != nil {
		t.Fatalf("argv start: %v", err)
	}
	if fake.lastCall != "start" || fake.command != "go run ./cmd/server" {
		t.Fatalf("argv start not threaded: %+v", fake)
	}

	for alias, want := range map[string]string{"tail": "logs", "kill": "stop", "ps": "list", "info": "status", "rm": "remove"} {
		args := []string{alias}
		if want != "list" {
			args = append(args, "p1")
		}
		if _, err := p.Execute(context.Background(), args); err != nil {
			t.Fatalf("alias %q: %v", alias, err)
		}
		if fake.lastCall != want {
			t.Fatalf("alias %q dispatched %q, want %q", alias, fake.lastCall, want)
		}
	}
}

func TestProcValidation(t *testing.T) {
	withFakeProc(t)
	p := NewBuiltinProcPlugin()

	cases := []struct {
		args []string
		want string
	}{
		{[]string{`{"cmd":"start","args":{}}`}, `"command" is required`},
		{[]string{`{"cmd":"logs","args":{}}`}, `"id" is required`},
		{[]string{`{"cmd":"restart","args":{}}`}, "unknown cmd"},
	}
	for _, c := range cases {
		if _, err := p.Execute(context.Background(), c.args); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Fatalf("args %v: err = %v, want contains %q", c.args, err, c.want)
		}
	}
	SetProcAdapter(nil)
	if _, err := p.Execute(context.Background(), []string{`{"cmd":"list"}`}); err == nil {
		t.Fatal("missing adapter must error, not panic")
	}
}

// TestProcCapsPerSubcommand pins the security posture: observation is
// read-only, lifecycle mutation is not — and unparseable args default to NOT
// read-only (fail closed).
func TestProcCapsPerSubcommand(t *testing.T) {
	p := NewBuiltinProcPlugin()

	readOnly := [][]string{
		{`{"cmd":"status","args":{"id":"p1"}}`},
		{`{"cmd":"logs","args":{"id":"p1"}}`},
		{`{"cmd":"list"}`},
	}
	for _, args := range readOnly {
		if !p.IsReadOnly(args) {
			t.Fatalf("%v must be read-only", args)
		}
	}
	mutating := [][]string{
		{`{"cmd":"start","args":{"command":"x"}}`},
		{`{"cmd":"stop","args":{"id":"p1"}}`},
		{`{"cmd":"remove","args":{"id":"p1"}}`},
		{`{not json`}, // fail closed
	}
	for _, args := range mutating {
		if p.IsReadOnly(args) {
			t.Fatalf("%v must NOT be read-only", args)
		}
	}
	if !p.IsConcurrencySafe([]string{`{"cmd":"list"}`}) || p.IsConcurrencySafe([]string{`{"cmd":"start","args":{"command":"x"}}`}) {
		t.Fatal("@proc observers run in parallel, mutators stay serial")
	}
}

// fakeInteractiveProcAdapter adds the ProcInteractive capability.
type fakeInteractiveProcAdapter struct {
	fakeProcAdapter
	ptyCommand string
	stdinID    string
	stdinText  string
	enter      bool
}

func (f *fakeInteractiveProcAdapter) StartPTY(command, dir string) (string, error) {
	f.lastCall, f.ptyCommand, f.dir = "startpty", command, dir
	return "started p1 on a pty", nil
}

func (f *fakeInteractiveProcAdapter) Stdin(id, text string, pressEnter bool) (string, error) {
	f.lastCall, f.stdinID, f.stdinText, f.enter = "stdin", id, text, pressEnter
	return "sent to " + id, nil
}

func TestProcPTYStartAndStdinDispatch(t *testing.T) {
	fake := &fakeInteractiveProcAdapter{}
	SetProcAdapter(fake)
	t.Cleanup(func() { SetProcAdapter(nil) })
	p := NewBuiltinProcPlugin()

	out, err := p.Execute(context.Background(), []string{`{"cmd":"start","args":{"command":"python3","pty":true}}`})
	if err != nil || !strings.Contains(out, "pty") {
		t.Fatalf("pty start: out=%q err=%v", out, err)
	}
	if fake.lastCall != "startpty" || fake.ptyCommand != "python3" {
		t.Fatalf("pty start not routed: %+v", fake)
	}

	// Plain start still uses the base path.
	if _, err := p.Execute(context.Background(), []string{`{"cmd":"start","args":{"command":"srv"}}`}); err != nil {
		t.Fatal(err)
	}
	if fake.lastCall != "start" {
		t.Fatalf("plain start must not use pty path: %+v", fake)
	}

	out, err = p.Execute(context.Background(), []string{`{"cmd":"stdin","args":{"id":"p1","text":"1+1"}}`})
	if err != nil || !strings.Contains(out, "sent to p1") {
		t.Fatalf("stdin: out=%q err=%v", out, err)
	}
	if fake.stdinText != "1+1" || !fake.enter {
		t.Fatalf("stdin args wrong: %+v", fake)
	}
	if _, err := p.Execute(context.Background(), []string{`{"cmd":"stdin","args":{"id":"p1","text":"x","no_enter":true}}`}); err != nil {
		t.Fatal(err)
	}
	if fake.enter {
		t.Fatal("no_enter must suppress Enter")
	}
	if _, err := p.Execute(context.Background(), []string{`{"cmd":"stdin","args":{"text":"x"}}`}); err == nil {
		t.Fatal("stdin without id must error")
	}
}

func TestProcPTYWithoutCapabilityErrors(t *testing.T) {
	SetProcAdapter(&fakeProcAdapter{})
	t.Cleanup(func() { SetProcAdapter(nil) })
	p := NewBuiltinProcPlugin()

	if _, err := p.Execute(context.Background(), []string{`{"cmd":"start","args":{"command":"python3","pty":true}}`}); err == nil {
		t.Fatal("pty start without capability must error")
	}
	if _, err := p.Execute(context.Background(), []string{`{"cmd":"stdin","args":{"id":"p1","text":"x"}}`}); err == nil {
		t.Fatal("stdin without capability must error")
	}
}

func TestProcStdinCapsNotReadOnly(t *testing.T) {
	p := NewBuiltinProcPlugin()
	if p.IsReadOnly([]string{`{"cmd":"stdin","args":{"id":"p1","text":"x"}}`}) {
		t.Fatal("stdin drives a process — never read-only")
	}
	if strings.TrimSpace(p.DescribeCall([]string{`{"cmd":"stdin","args":{"id":"p1","text":"x"}}`})) == "" {
		t.Fatal("DescribeCall(stdin) empty")
	}
}

func TestProcPTYFlattenedArgv(t *testing.T) {
	fake := &fakeInteractiveProcAdapter{}
	SetProcAdapter(fake)
	t.Cleanup(func() { SetProcAdapter(nil) })
	p := NewBuiltinProcPlugin()

	// The agent flattener turns {"cmd":"start","args":{"command":"python3",
	// "pty":true}} into ["start","--command","python3","--pty"].
	if _, err := p.Execute(context.Background(), []string{"start", "--command", "python3", "--pty"}); err != nil {
		t.Fatal(err)
	}
	if fake.lastCall != "startpty" || fake.ptyCommand != "python3" {
		t.Fatalf("flattened pty start not routed: %+v", fake)
	}
	// stdin flattened: ["stdin","--id","p1","--text","1+1"].
	if _, err := p.Execute(context.Background(), []string{"stdin", "--id", "p1", "--text", "1+1"}); err != nil {
		t.Fatal(err)
	}
	if fake.stdinID != "p1" || fake.stdinText != "1+1" {
		t.Fatalf("flattened stdin not routed: %+v", fake)
	}
}

func TestProcWaitAndGrep(t *testing.T) {
	fake := withFakeProc(t)
	p := NewBuiltinProcPlugin()
	out, err := p.Execute(context.Background(), []string{`{"cmd":"wait","args":{"id":"p1","pattern":"listening on","timeout":90}}`})
	if err != nil || out != "waited" || fake.lastCall != "wait" || fake.pattern != "listening on" || fake.timeout != 90 {
		t.Fatalf("wait: %q %v %+v", out, err, fake)
	}
	if _, err := p.Execute(context.Background(), []string{"wait", "--id", "p1"}); err != nil || fake.timeout != procWaitDefault {
		t.Fatalf("wait default timeout: %v %d", err, fake.timeout)
	}
	if _, err := p.Execute(context.Background(), []string{`{"cmd":"until","args":{"id":"p1","timeout":99999}}`}); err != nil || fake.timeout != procWaitMax || fake.lastCall != "wait" {
		t.Fatalf("wait alias and max: %v %d %s", err, fake.timeout, fake.lastCall)
	}
	if _, err := p.Execute(context.Background(), []string{`{"cmd":"wait","args":{}}`}); err == nil {
		t.Fatal("wait without id must error")
	}
	out, err = p.Execute(context.Background(), []string{`{"cmd":"logs","args":{"id":"p1","grep":"error|panic","tail":30}}`})
	if err != nil || out != "grepped" || fake.lastCall != "grep" || fake.pattern != "error|panic" || fake.tail != 30 {
		t.Fatalf("logs grep: %q %v %+v", out, err, fake)
	}
	if _, err := p.Execute(context.Background(), []string{`{"cmd":"logs","args":{"id":"p1"}}`}); err != nil || fake.lastCall != "logs" {
		t.Fatalf("plain logs stays on Logs: %v %s", err, fake.lastCall)
	}
	if _, err := p.Execute(context.Background(), []string{`{"cmd":"logs","args":{"id":"p1","tail":"fifty"}}`}); err == nil || !strings.Contains(err.Error(), "do not parse") {
		t.Fatalf("a wrongly typed field is reported, not dropped: %v", err)
	}
	if !p.IsReadOnly([]string{`{"cmd":"wait","args":{"id":"p1"}}`}) || p.IsConcurrencySafe([]string{`{"cmd":"wait","args":{"id":"p1"}}`}) == false {
		// wait observes; it may share a parallel batch with other reads.
		t.Fatal("wait is read-only")
	}
	if d := p.DescribeCall([]string{`{"cmd":"wait","args":{"id":"p1"}}`}); !strings.Contains(d, "p1") {
		t.Fatalf("describe wait = %q", d)
	}
}
