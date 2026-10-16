/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/diillson/chatcli/cli/agent/runs"
	"github.com/diillson/chatcli/cli/agentevents"
	"github.com/diillson/chatcli/cli/coder"
	"github.com/diillson/chatcli/pkg/pulse"
	"github.com/diillson/chatcli/pkg/pulse/pulsetest"
	"go.uber.org/zap"
)

func TestPulseRunEventHangsFromEntrySpansOnly(t *testing.T) {
	cases := []struct {
		info runs.Info
		want string
	}{
		{runs.Info{ID: "run-1", PulseParent: "turn-4"}, "turn-4"},
		{runs.Info{ID: "run-1", PulseParent: "rpc-2"}, "rpc-2"},
		{runs.Info{ID: "run-1", ParentID: "run-0", PulseParent: "tool-9"}, "run-0"},
		{runs.Info{ID: "run-1", ParentID: "run-0", PulseParent: "pattern-1"}, "run-0"},
		{runs.Info{ID: "run-1"}, pulseSessionNodeID},
	}
	for _, c := range cases {
		if got := pulseRunEvent(c.info).Parent; got != c.want {
			t.Errorf("%+v → parent %q, want %q", c.info, got, c.want)
		}
	}
}

func TestPulseCallerFromPrefersTheInnermostNode(t *testing.T) {
	g := runs.NewRegistry(4)
	runCtx, run := g.Begin(context.Background(), runs.Info{Agent: "coder"})
	defer run.End(nil)
	if got := pulseCallerFrom(runCtx); got != run.ID() {
		t.Fatalf("caller = %q, want the run", got)
	}
	if got := pulseCallerFrom(pulse.WithParent(runCtx, "tool-3")); got != "tool-3" {
		t.Fatalf("caller = %q, want the tool span", got)
	}
	if got := pulseCallerFrom(runs.WithRun(context.Background(), run)); got != run.ID() {
		t.Fatalf("a run attached without Begin still names the caller: %q", got)
	}
	if got := pulseCallerFrom(context.Background()); got != "" {
		t.Fatalf("caller = %q, want none", got)
	}
}

func TestPulsePolicyVerdictsAndApprovals(t *testing.T) {
	rec := pulsetest.Record(t)
	a := &AgentMode{}
	a.lastPolicyMatch = &coder.Rule{Pattern: "@coder exec curl https://secret.example/token=abc", Action: coder.ActionDeny}
	a.pulsePolicyVerdict("@coder", coder.ActionDeny, pulsePolicySourceRule)
	deny := rec.WaitFor(t, "deny verdict", func(ev pulse.Event) bool {
		return ev.Name == "policy" && ev.Attrs["decision"] == string(coder.ActionDeny)
	})
	if deny.Status != pulse.StatusBlocked || deny.Attrs["rule"] != "@coder exec" || deny.Attrs["source"] != "rule" || deny.Attrs["tool"] != "@coder" {
		t.Fatalf("deny = %+v", deny)
	}
	a.pulsePolicyVerdict("@read", coder.ActionAllow, pulsePolicySourceAutomode)
	a.pulsePolicyVerdict("@moa", coder.ActionAsk, pulsePolicySourceDefault)
	rec.WaitFor(t, "allow verdict", func(ev pulse.Event) bool {
		return ev.Name == "policy" && ev.Attrs["tool"] == "@read" && ev.Status == pulse.StatusOK && ev.Attrs["rule"] == ""
	})
	rec.WaitFor(t, "ask verdict", func(ev pulse.Event) bool {
		return ev.Name == "policy" && ev.Attrs["tool"] == "@moa" && ev.Status == pulse.StatusRunning
	})

	answers := map[coder.SecurityDecision][2]string{
		coder.DecisionRunOnce:     {"run_once", pulse.StatusOK},
		coder.DecisionAllowAlways: {"allow_always", pulse.StatusOK},
		coder.DecisionDenyOnce:    {"deny_once", pulse.StatusBlocked},
		coder.DecisionDenyForever: {"deny_forever", pulse.StatusBlocked},
		coder.DecisionCanceled:    {"cancelled", pulse.StatusCancelled},
	}
	for d, want := range answers {
		a.pulseApprovalEnd(a.pulseApprovalBegin("@x"), d)
		rec.WaitFor(t, "approval "+want[0], func(ev pulse.Event) bool {
			return ev.Name == "approval" && ev.Phase == pulse.PhaseEnd && ev.Attrs["answer"] == want[0] && ev.Status == want[1]
		})
	}

	client := []struct {
		d      agentevents.PermissionDecision
		asked  bool
		err    error
		answer string
	}{
		{"", false, nil, "no_dialog"},
		{"", true, agentevents.ErrPermissionTimeout, "timeout"},
		{"", true, errors.New("pipe"), "failed"},
		{agentevents.PermissionAllowOnce, true, nil, "allowed"},
		{agentevents.PermissionDenyAlways, true, nil, "denied"},
	}
	for _, c := range client {
		pulseClientApprovalEnd(a.pulseApprovalBegin("@y"), c.d, c.asked, c.err)
		rec.WaitFor(t, "client approval "+c.answer, func(ev pulse.Event) bool {
			return ev.Name == "approval" && ev.Phase == pulse.PhaseEnd && ev.Attrs["answer"] == c.answer
		})
	}
}

func TestBudgetBlockIsReported(t *testing.T) {
	rec := pulsetest.Record(t)
	t.Setenv("CHATCLI_SESSION_BUDGET_USD", "1")
	t.Setenv("CHATCLI_BUDGET_HARD_STOP", "true")
	ct := NewCostTrackerAt(t.TempDir())
	ct.totalCostUSD = 2
	c := &ChatCLI{costTracker: ct}
	if c.budgetBlockedErr() == nil {
		t.Fatal("an exhausted budget with hard stop must block")
	}
	ev := rec.WaitFor(t, "budget point", pulsetest.Is(pulse.KindPattern, "budget", pulse.PhasePoint))
	if ev.Status != pulse.StatusBlocked || ev.Attrs["spent_usd"] != "2.0000" {
		t.Fatalf("budget = %+v", ev)
	}
}

// fakeTracedPlugin is a builtin that records the pulse parent it ran under.
type fakeTracedPlugin struct {
	parent string
	err    error
}

func (f *fakeTracedPlugin) Name() string        { return "@fake" }
func (f *fakeTracedPlugin) Description() string { return "" }
func (f *fakeTracedPlugin) Usage() string       { return "" }
func (f *fakeTracedPlugin) Version() string     { return "1" }
func (f *fakeTracedPlugin) Path() string        { return "" }
func (f *fakeTracedPlugin) Schema() string      { return "" }
func (f *fakeTracedPlugin) Execute(ctx context.Context, _ []string) (string, error) {
	f.parent = pulse.ParentFrom(ctx)
	return "12345", f.err
}
func (f *fakeTracedPlugin) ExecuteWithStream(ctx context.Context, args []string, _ func(string)) (string, error) {
	return f.Execute(ctx, args)
}

func TestTracedExecuteIsAToolNodeUnderItsCaller(t *testing.T) {
	rec := pulsetest.Record(t)
	p := &fakeTracedPlugin{}
	ctx := pulse.WithParent(context.Background(), "turn-1")
	if _, err := tracedExecute(ctx, p, []string{`{"cmd":"recall","args":{"query":"secret"}}`}, "chat"); err != nil {
		t.Fatal(err)
	}
	end := rec.WaitFor(t, "tool end", pulsetest.Is(pulse.KindTool, "@fake", pulse.PhaseEnd))
	if end.Parent != "turn-1" || end.Attrs["origin"] != "chat" || end.Attrs["subcmd"] != "recall" || end.Attrs["out_bytes"] != "5" {
		t.Fatalf("tool = %+v", end)
	}
	if p.parent != end.ID {
		t.Fatalf("work the tool starts must hang from it: %q vs %q", p.parent, end.ID)
	}
	for _, ev := range rec.Events() {
		for _, v := range ev.Attrs {
			if strings.Contains(v, "secret") {
				t.Fatal("arguments must never reach the bus")
			}
		}
	}
	p.err = errors.New("boom")
	_, _ = tracedExecute(context.Background(), p, nil, "worker")
	rec.WaitFor(t, "failed tool", func(ev pulse.Event) bool {
		return ev.Name == "@fake" && ev.Phase == pulse.PhaseEnd && ev.Status == pulse.StatusError && ev.Attrs["origin"] == "worker"
	})
}

func TestToolSubcmd(t *testing.T) {
	cases := map[string][]string{
		"":       nil,
		"read":   {"read", "--file", "x"},
		"search": {`{"cmd":"search","args":{}}`},
		"-":      {"--flag"},
		"x":      {`{"args":{}}`},
		"y":      {`{not json`},
		"url":    {"https://api.example.com/v1?api_key=SECRET"},
		"words":  {"my private question"},
		"jsonv":  {`{"cmd":"https://x.example/?token=1"}`},
		"upper":  {"Read"},
		"long":   {"averyveryveryverylongsubcommandname"},
	}
	want := map[string]string{"": "", "read": "read", "search": "search", "-": "", "x": "", "y": "",
		"url": "", "words": "", "jsonv": "", "upper": "", "long": ""}
	for k, argv := range cases {
		if got := toolSubcmd(argv); got != want[k] {
			t.Errorf("toolSubcmd(%v) = %q, want %q", argv, got, want[k])
		}
	}
}

func TestMoACommandIsATurnWithItsPanelUnderIt(t *testing.T) {
	rec := pulsetest.Record(t)
	mgr := &moaFakeManager{
		minimalManager: &minimalManager{providers: []string{"a", "b", "agg"}},
		answers:        map[string]string{"a": "Yes. A.", "b": "No. B.", "agg": "FINAL"},
	}
	c := newMoaCLI(mgr, "agg", "m")
	ch, err := NewContextHandlerAt(t.TempDir(), zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	c.contextHandler = ch
	t.Setenv("CHATCLI_MOA_MODELS", "a:m1,b:m2")
	t.Setenv("CHATCLI_MOA_LAYERS", "")
	captureStdout(t, func() { err = c.runMoACommand(context.Background(), "/moa --layers 2 q?") })
	if err != nil {
		t.Fatal(err)
	}
	turn := rec.WaitFor(t, "moa turn", pulsetest.Is(pulse.KindTurn, pulseTurnMoA, pulse.PhaseEnd))
	if turn.Status != pulse.StatusOK || turn.Attrs["members"] != "2" || turn.Attrs["layers"] != "2" || turn.Attrs["aggregator"] != "agg:m" {
		t.Fatalf("turn = %+v", turn)
	}
	var panel, agg *runs.Info
	recent := runs.Default().Recent(200)
	for _, info := range recent {
		if info.Agent == "moa" && info.PulseParent == turn.ID {
			info := info
			panel = &info
		}
	}
	for _, info := range recent {
		if panel != nil && info.ParentID == panel.ID && strings.HasSuffix(info.Agent, "(aggregator)") {
			info := info
			agg = &info
		}
	}
	if panel == nil || panel.Turn != 2 || panel.MaxTurns != 2 {
		t.Fatalf("the panel run should hang from the turn and show its rounds: %+v", panel)
	}
	if agg == nil || agg.ParentID != panel.ID {
		t.Fatalf("the aggregator run should sit under the panel: %+v", agg)
	}
}

func TestDashSummaryShowsWhoCalledANode(t *testing.T) {
	n := &pulse.Node{Kind: pulse.KindLLM, Name: "gpt", Calls: 2, ParentNode: "turn:chat"}
	if got := renderDashNode(n); !strings.HasSuffix(got, "← turn:chat") {
		t.Fatalf("render = %q", got)
	}
	if got := renderDashNode(&pulse.Node{Kind: pulse.KindLLM, Name: "gpt"}); strings.Contains(got, "←") {
		t.Fatalf("no caller, no arrow: %q", got)
	}
}

func TestPulseToolContextNamesTheToolSpan(t *testing.T) {
	_ = pulsetest.Record(t)
	a := &AgentMode{}
	if ctx := a.pulseToolContext(context.Background(), "missing"); pulse.ParentFrom(ctx) != "" {
		t.Fatal("an unknown call leaves ctx as it is")
	}
	a.pulseToolBegin(agentevents.ToolCall{ID: "c1", Name: "@webfetch"})
	ctx := a.pulseToolContext(context.Background(), "c1")
	if id := pulse.ParentFrom(ctx); pulse.KindOf(id) != pulse.KindTool {
		t.Fatalf("parent = %q, want the tool's span", id)
	}
	a.pulseToolEnd("c1", nil)
}

func TestProcessRecorderIsSharedAndOwned(t *testing.T) {
	pulseProcMu.Lock()
	busy := pulseProcCtl != nil
	pulseProcMu.Unlock()
	if busy {
		t.Skip("another test owns the process recorder")
	}
	t.Setenv("HOME", t.TempDir()) // the recorder's spool root lives under HOME
	first := &ChatCLI{logger: zap.NewNop()}
	second := &ChatCLI{logger: zap.NewNop()}
	first.initPulse(context.Background(), "repl")
	second.initPulse(context.Background(), "gateway")
	if !first.pulseOwner || second.pulseOwner || first.pulse != second.pulse {
		t.Fatalf("the second ChatCLI must share the first one's recorder without owning it")
	}
	if stop := StartPulseRecorder(context.Background(), "server", zap.NewNop()); stop == nil {
		t.Fatal("a recorder already running hands back a no-op stop")
	} else {
		stop()
	}
	second.shutdownPulse()
	pulseProcMu.Lock()
	alive := pulseProcCtl != nil
	pulseProcMu.Unlock()
	if !alive {
		t.Fatal("a non-owner's shutdown must leave the recorder running")
	}
	first.shutdownPulse()
	pulseProcMu.Lock()
	alive = pulseProcCtl != nil
	pulseProcMu.Unlock()
	if alive {
		t.Fatal("the owner's shutdown stops the recorder")
	}
}
