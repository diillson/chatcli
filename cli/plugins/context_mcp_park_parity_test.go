/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package plugins

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/diillson/chatcli/cli/agent/park"
)

// Every call below is the argv the agent loop hands the plugin after
// flattening {cmd,args}: keys sorted, true as a bare flag, arrays repeated.

type fakeContextMaintainer struct {
	fakeContextAdapter
	opts        ContextAttachOptions
	tags        []string
	maintained  string
	maintainArg string
}

func (f *fakeContextMaintainer) CreateTagged(name, _ string, _ []string, _ string, tags []string, _ bool) (string, error) {
	f.createName, f.tags = name, tags
	return "created", nil
}
func (f *fakeContextMaintainer) AttachWith(name string, opts ContextAttachOptions) (string, error) {
	f.attachName, f.opts = name, opts
	return "attached", nil
}
func (f *fakeContextMaintainer) maintain(verb, name string) (string, error) {
	f.maintained, f.maintainArg = verb, name
	return verb, nil
}
func (f *fakeContextMaintainer) Refresh(_ context.Context, n string) (string, error) {
	return f.maintain("refresh", n)
}
func (f *fakeContextMaintainer) Watch(n string) (string, error)   { return f.maintain("watch", n) }
func (f *fakeContextMaintainer) Unwatch(n string) (string, error) { return f.maintain("unwatch", n) }

// assertDocumented checks Usage and Schema both name each verb.
func assertDocumented(t *testing.T, p interface {
	Usage() string
	Schema() string
}, verbs ...string) {
	t.Helper()
	for _, verb := range verbs {
		if !strings.Contains(p.Usage(), "\n  "+verb+" ") || !strings.Contains(p.Schema(), `"name":"`+verb+`"`) {
			t.Errorf("Usage/Schema must document %q", verb)
		}
	}
}

func TestContextFlattenedParityVerbs(t *testing.T) {
	f := &fakeContextMaintainer{}
	SetContextAdapter(f)
	t.Cleanup(func() { SetContextAdapter(nil) })
	p := NewBuiltinContextPlugin()
	run := func(argv ...string) (string, error) { return p.Execute(context.Background(), argv) }

	if _, err := run("attach", "--chunks", "1", "--chunks", "3", "--full", "--name", "repo", "--priority", "5", "--weight", "1.5"); err != nil {
		t.Fatalf("attach: %v", err)
	}
	want := ContextAttachOptions{Priority: 5, Chunks: []int{1, 3}, Weight: 1.5, Full: true}
	if f.attachName != "repo" || !reflect.DeepEqual(f.opts, want) {
		t.Fatalf("attach options = %q %+v, want %+v", f.attachName, f.opts, want)
	}
	if _, err := run("attach", "--name", "repo", "--rag"); err != nil || f.opts.RagTopK != contextDefaultRagTopK {
		t.Fatalf("bare --rag: opts=%+v err=%v", f.opts, err)
	}
	if _, err := run(`{"cmd":"attach","args":{"name":"repo","chunks":[2,4],"rag":6}}`); err != nil || f.opts.RagTopK != 6 || !reflect.DeepEqual(f.opts.Chunks, []int{2, 4}) {
		t.Fatalf("JSON envelope: opts=%+v err=%v", f.opts, err)
	}
	for _, bad := range [][]string{
		{"attach", "--name", "repo", "--rag", "0"},
		{"attach", "--name", "repo", "--rag", "many"},
		{"attach", "--name", "repo", "--priority", "high"},
		{"attach", "--name", "repo", "--weight", "heavy"},
		{"attach", "--chunks", "one", "--name", "repo"},
		{"attach", "--mode", "full", "--name", "repo"},
		{"attach"},
		{"refresh"},
		{"unwatch", "--name", "x", "--force"},
	} {
		f.attachName = ""
		if _, err := run(bad...); err == nil || f.attachName != "" {
			t.Errorf("%v must be reported and do nothing (err=%v)", bad, err)
		}
	}

	if _, err := run("create", "--name", "d", "--paths", "/tmp/a", "--tags", "api", "--tags", "go"); err != nil || !reflect.DeepEqual(f.tags, []string{"api", "go"}) {
		t.Fatalf("create tags = %v err=%v", f.tags, err)
	}
	for verb, name := range map[string]string{"refresh": "d", "watch": "d", "unwatch": "d"} {
		if _, err := run(verb, "--name", name); err != nil || f.maintained != verb || f.maintainArg != name {
			t.Errorf("%s: got %s(%q) err=%v", verb, f.maintained, f.maintainArg, err)
		}
	}
	if _, err := run("watch"); err != nil || f.maintainArg != "" {
		t.Errorf("watch without name lists: arg=%q err=%v", f.maintainArg, err)
	}
	if !p.IsReadOnly([]string{"watch"}) || p.IsReadOnly([]string{"watch", "--name", "d"}) || p.IsReadOnly([]string{"refresh", "--name", "d"}) {
		t.Error("only the watch listing is read-only")
	}
	if out, err := run("attached"); err != nil || out != "status" || !f.statused {
		t.Errorf("attached must reach Status: out=%q err=%v", out, err)
	}
	assertDocumented(t, p, strings.Split(contextValidCmds, "|")...)
}

// An adapter written before the parity verbs keeps working for what it has
// and refuses the rest instead of dropping the option.
func TestContextLegacyAdapterRefusesNewOptions(t *testing.T) {
	f := withFakeContextAdapter(t)
	p := NewBuiltinContextPlugin()
	if _, err := p.Execute(context.Background(), []string{"attach", "--name", "x", "--rag", "4"}); err != nil || f.attachRag != 4 {
		t.Fatalf("plain attach: rag=%d err=%v", f.attachRag, err)
	}
	for _, argv := range [][]string{
		{"attach", "--full", "--name", "x"},
		{"create", "--name", "x", "--paths", "p", "--tags", "t"},
		{"refresh", "--name", "x"},
	} {
		if _, err := p.Execute(context.Background(), argv); err == nil || !strings.Contains(err.Error(), "not available") {
			t.Errorf("%v: err=%v", argv, err)
		}
	}
}

type fakeMCPServerAdapter struct{ restarted, calls string }

func (f *fakeMCPServerAdapter) Login(context.Context, string) (string, error) {
	f.calls += "login;"
	return "", nil
}
func (f *fakeMCPServerAdapter) Logout(string) (string, error)  { return "", nil }
func (f *fakeMCPServerAdapter) Status() (string, error)        { return "status", nil }
func (f *fakeMCPServerAdapter) Tools(s string) (string, error) { return "tools:" + s, nil }
func (f *fakeMCPServerAdapter) Restart(s string) (string, error) {
	f.restarted = s
	return "restarted " + s, nil
}

type legacyMCPAdapter struct{ MCPAuthAdapter }

func TestMCPLoginFlattenedRestartAndTools(t *testing.T) {
	f := &fakeMCPServerAdapter{}
	SetMCPAuthAdapter(f)
	t.Cleanup(func() { SetMCPAuthAdapter(nil) })
	p := NewBuiltinMCPLoginPlugin()
	run := func(argv ...string) (string, error) { return p.Execute(context.Background(), argv) }

	if out, err := run("restart", "--server", "fs"); err != nil || f.restarted != "fs" || out != "restarted fs" {
		t.Fatalf("restart: out=%q restarted=%q err=%v", out, f.restarted, err)
	}
	if out, err := run("tools", "--server", "fs"); err != nil || out != "tools:fs" {
		t.Fatalf("tools: out=%q err=%v", out, err)
	}
	if out, err := run("tools"); err != nil || out != "tools:" {
		t.Fatalf("tools without server: out=%q err=%v", out, err)
	}
	f.restarted = ""
	for _, bad := range [][]string{
		{"restart"}, {"restart", "--server", "all"}, {"restart", "--server", "*"},
		{"restart", "--force", "--server", "fs"}, {"reload", "--server", "fs"},
	} {
		if _, err := run(bad...); err == nil || f.restarted != "" || f.calls != "" {
			t.Errorf("%v must be an error that restarts and logs in nothing (err=%v)", bad, err)
		}
	}
	if !p.IsReadOnly([]string{"tools", "--server", "fs"}) || p.IsReadOnly([]string{"restart", "--server", "fs"}) {
		t.Error("tools is read-only, restart is not")
	}
	if p.DescribeCall([]string{"restart", "--server", "fs"}) == p.DescribeCall([]string{"tools"}) {
		t.Error("restart and tools need their own spinner text")
	}
	assertDocumented(t, p, "restart", "tools")
	SetMCPAuthAdapter(legacyMCPAdapter{f})
	if _, err := run("restart", "--server", "fs"); err == nil || !strings.Contains(err.Error(), "not available") {
		t.Errorf("legacy adapter: err=%v", err)
	}
}

type fakeParkManager struct {
	fakeParkCanceller
	resumed, discarded string
}

func (f *fakeParkManager) ResumePark(tok string) (string, error) {
	f.resumed = tok
	return "resumed", nil
}
func (f *fakeParkManager) DiscardPark(tok string) (string, error) {
	f.discarded = tok
	return "gone", nil
}

func TestParkFlattenedResumeDiscard(t *testing.T) {
	t.Setenv("CHATCLI_PARK_DIR", t.TempDir())
	snap := saveTestPark(t, "ci", time.Now())
	f := &fakeParkManager{}
	SetParkCanceller(f)
	t.Cleanup(func() { SetParkCanceller(nil) })
	p := NewBuiltinParkPlugin()
	run := func(argv ...string) (string, error) { return p.Execute(context.Background(), argv) }

	if out, err := run("resume", "--token", snap.Token[:8]); err != nil || f.resumed != snap.Token || !strings.Contains(out, "resumed") {
		t.Fatalf("resume: out=%q resumed=%q err=%v", out, f.resumed, err)
	}
	if _, err := run("discard"); err == nil || f.discarded != "" {
		t.Fatalf("discard without a token must not pick the newest park (err=%v)", err)
	}
	if _, err := run("discard", "--token", snap.Token[:8]); err != nil || f.discarded != snap.Token {
		t.Fatalf("discard: discarded=%q err=%v", f.discarded, err)
	}
	for _, bad := range [][]string{{"resume", "--token", "zzzz"}, {"resume", "--reason", "x"}, {"prune"}} {
		if _, err := run(bad...); err == nil {
			t.Errorf("%v must fail", bad)
		}
	}
	SetParkCanceller(&fakeParkCanceller{})
	if _, err := run("resume"); err == nil || !strings.Contains(err.Error(), "no park runtime") {
		t.Errorf("canceller without the manager half: err=%v", err)
	}
	assertDocumented(t, p, "resume", "discard")
}

// {"duration":300} is flattened to "--duration 300", read back as a number.
func TestParkFlattenedNumericDurationNamesTheUnit(t *testing.T) {
	p := NewBuiltinParkPlugin()
	for _, argv := range [][]string{
		{"delay", "--duration", "300"},
		{"for_url", "--deadline", "10m", "--interval", "30", "--url", "https://example.test"},
		{"until", "--when", "--note", "x"},
	} {
		_, err := p.Execute(context.Background(), argv)
		if err == nil || strings.Contains(err.Error(), "required") || !strings.Contains(err.Error(), "duration") {
			t.Errorf("%v: err=%v", argv, err)
		}
	}
	_, err := p.Execute(context.Background(), []string{"delay", "--duration", "300s"})
	if req, ok := park.AsParkError(err); !ok || req.Delay != 300*time.Second {
		t.Errorf("a duration with a unit must park: %v", err)
	}
}

// /context attach --weight 0 is an error; the tool used to accept it.
func TestContextAttachWeightMatchesCommand(t *testing.T) {
	f := &fakeContextMaintainer{}
	SetContextAdapter(f)
	t.Cleanup(func() { SetContextAdapter(nil) })
	p := NewBuiltinContextPlugin()
	for _, w := range []string{"0", "-1", "10.5"} {
		if _, err := p.Execute(context.Background(), []string{"attach", "--name", "repo", "--weight", w}); err == nil || f.attachName != "" {
			t.Errorf("weight %s must be rejected (err=%v)", w, err)
		}
	}
}
