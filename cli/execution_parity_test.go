/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"context"
	"fmt"
	"github.com/diillson/chatcli/cli/agent/workers"
	"github.com/diillson/chatcli/cli/coder"
	"github.com/diillson/chatcli/cli/mcp"
	"github.com/diillson/chatcli/cli/mcp/triggers"
	"github.com/diillson/chatcli/cli/plugins"
	"github.com/diillson/chatcli/cli/taskgraph"
	"github.com/diillson/chatcli/i18n"
	"go.uber.org/zap"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// gatedDispatcher holds every dispatch until released or (unless deaf)
// canceled, first asking its policy hook as a worker's gated call would.
type gatedDispatcher struct {
	release chan struct{}
	deaf    bool
	policy  func(ctx context.Context) // called with the run's context
}

func (d gatedDispatcher) Dispatch(ctx context.Context, calls []workers.AgentCall) []workers.AgentResult {
	if d.policy != nil {
		d.policy(ctx)
	}
	cancel := ctx.Done()
	if d.deaf {
		cancel = nil
	}
	select {
	case <-d.release:
	case <-cancel:
	}
	return passDispatcher{}.Dispatch(ctx, calls)
}
func (gatedDispatcher) SetCallUsageRecorder(workers.CallUsageRecorder) {}

// waitTaskGraphBackground blocks until the background slot is free.
func waitTaskGraphBackground(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for plugins.TaskGraphBackgroundActiveID() != "" {
		if time.Now().After(deadline) {
			t.Fatal("background run did not finish")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// plan/run/retry from a file: run and retry return at once with the run id,
// and status shows the background output the tool's status shows.
func TestTaskGraphCommandPlanRunRetry(t *testing.T) {
	t.Cleanup(func() { plugins.CancelTaskGraphBackgroundRun() })
	a := newTestTaskGraphAdapter(t)
	cli := &ChatCLI{taskGraphAdapter: a}
	run := func(line string) string {
		return captureStdout(t, func() { cli.handleTaskGraphCommand(context.Background(), line) })
	}
	plan := filepath.Join(t.TempDir(), "plan.json")
	if err := os.WriteFile(plan, []byte(testPlanJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	// Without the agent's workers nothing may start (and nothing is saved).
	if out := run("/taskgraph run " + plan); !strings.Contains(out, i18n.T("taskgraph.err.no_dispatcher")) {
		t.Fatalf("no dispatcher: %q", out)
	}
	if out := run("/taskgraph status"); !strings.Contains(out, i18n.T("taskgraph.err.no_runs")) {
		t.Fatalf("a refused run must not persist a plan: %q", out)
	}
	gate := gatedDispatcher{release: make(chan struct{})}
	a.dispatcherFn = func() (taskGraphDispatcher, error) { return gate, nil }
	for line, want := range map[string]string{
		"/taskgraph run":               i18n.T("taskgraph.cmd.file_required", "run"),
		"/taskgraph plan":              i18n.T("taskgraph.cmd.file_required", "plan"),
		"/taskgraph run missing.json":  "missing.json",
		"/taskgraph prune soon":        i18n.T("taskgraph.err.bad_retention", "soon"),
		"/taskgraph cancel":            i18n.T("taskgraph.err.no_active_run"),
		"/taskgraph retry tg-1 T1 T2":  i18n.T("execparity.extra_arg", "/taskgraph retry", "T2"),
		"/taskgraph retry tg-missing":  "/taskgraph: ",
		"/taskgraph show T1 tg-absent": "/taskgraph: ",
	} {
		if out := run(line); !strings.Contains(out, want) || strings.Contains(out, "@taskgraph") {
			t.Errorf("%s: want %q and no tool wording in %q", line, want, out)
		}
	}
	if out := run("/taskgraph plan " + plan); !strings.Contains(out, "/taskgraph run tg-") {
		t.Fatalf("plan: %q", out)
	}
	rows, err := taskgraph.ListRuns(mustBaseDir(t, a))
	if err != nil || len(rows) != 1 {
		t.Fatalf("plan must persist one run: %v %v", rows, err)
	}
	planned := rows[0].RunID
	for line, want := range map[string]string{
		"/taskgraph retry":    i18n.T("taskgraph.err.retry_pick", planned, 0, ""),
		"/taskgraph retry T9": i18n.T("taskgraph.err.no_task", "T9", planned),
		"/taskgraph retry T1": i18n.T("taskgraph.err.retry_not_failed", "T1", "pending"),
		"/taskgraph show T9":  i18n.T("taskgraph.err.no_task", "T9", planned),
	} {
		if out := run(line); !strings.Contains(out, want) {
			t.Errorf("%s: want %q in %q", line, want, out)
		}
	}
	// run returns while the graph is still executing.
	if out := run("/taskgraph run " + planned); !strings.Contains(out, i18n.T("taskgraph.cmd.run_started", planned, planned)) {
		t.Fatalf("run: %q", out)
	}
	if plugins.TaskGraphBackgroundActiveID() != planned {
		t.Fatal("run must hand the graph to the tool's background slot and return")
	}
	if out := run("/taskgraph status"); !strings.Contains(out, "background run "+planned+": running") {
		t.Fatalf("status must show the background run: %q", out)
	}
	for _, line := range []string{"/taskgraph run " + plan, "/taskgraph retry"} {
		if out := run(line); !strings.Contains(out, i18n.T("taskgraph.cmd.background_busy", planned)) {
			t.Errorf("%s while a run is active: %q", line, out)
		}
	}
	close(gate.release)
	waitTaskGraphBackground(t)
	if out := run("/taskgraph " + planned); !strings.Contains(out, "status=done") || !strings.Contains(out, "output tail") {
		t.Fatalf("finished run: %q", out)
	}
	// A wrong task id stays an error after a background run has existed.
	if out := run("/taskgraph show T9"); !strings.Contains(out, i18n.T("taskgraph.err.no_task", "T9", planned)) || strings.Contains(out, "final report") {
		t.Fatalf("show of an unknown task must be an error, not the run's report: %q", out)
	}
	plugins.SetTaskGraphAdapter(a)
	t.Cleanup(func() { plugins.SetTaskGraphAdapter(nil) })
	if out, err := plugins.NewBuiltinTaskGraphPlugin().Execute(context.Background(), []string{"show", "--task", "T9"}); err == nil {
		t.Fatalf("@taskgraph show of an unknown task must error, got %q", out)
	}
	// During a foreground tool run, run refuses and persists nothing.
	a.mu.Lock()
	a.active, a.activeID = &taskgraph.Engine{}, "tg-foreground"
	a.mu.Unlock()
	if out := run("/taskgraph run " + plan); !strings.Contains(out, i18n.T("taskgraph.err.busy")) {
		t.Fatalf("run during a foreground run: %q", out)
	}
	a.mu.Lock()
	a.active, a.activeID = nil, ""
	a.mu.Unlock()
	if rows, _ := taskgraph.ListRuns(mustBaseDir(t, a)); len(rows) != 1 {
		t.Fatalf("a refused run must not leave a run directory: %d runs", len(rows))
	}
	// retry with no task id re-opens the run's only failed task.
	store, err := taskgraph.OpenRun(mustBaseDir(t, a), planned)
	if err != nil {
		t.Fatal(err)
	}
	g, err := store.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	g.Status, g.Tasks[0].Status = taskgraph.StatusFailed, taskgraph.StatusFailed
	if err := store.SaveState(g); err != nil {
		t.Fatal(err)
	}
	out := run("/taskgraph retry " + planned)
	if !strings.Contains(out, i18n.T("taskgraph.cmd.retry_reopened", "T1")) || !strings.Contains(out, i18n.T("taskgraph.cmd.run_started", planned, planned)) {
		t.Fatalf("retry: %q", out)
	}
	waitTaskGraphBackground(t)
	if out := run("/taskgraph status " + planned); !strings.Contains(out, "status=done") {
		t.Fatalf("retried run: %q", out)
	}
	// cancel reaches the background slot and returns without waiting.
	gate = gatedDispatcher{release: make(chan struct{}), deaf: true}
	if out := run("/taskgraph run " + plan); !strings.Contains(out, "tg-") {
		t.Fatalf("second run: %q", out)
	}
	active := plugins.TaskGraphBackgroundActiveID()
	began := time.Now()
	if out := run("/taskgraph cancel"); !strings.Contains(out, i18n.T("taskgraph.cmd.background_canceled", active)) {
		t.Fatalf("cancel: %q", out)
	}
	if waited := time.Since(began); waited > 3*time.Second || plugins.TaskGraphBackgroundActiveID() != active {
		t.Fatalf("cancel must request and return (waited %s)", waited)
	}
	close(gate.release)
	waitTaskGraphBackground(t)
	if out := run("/taskgraph prune all"); !strings.Contains(out, i18n.T("taskgraph.cmd.pruned_all", 2)) {
		t.Fatalf("prune all: %q", out)
	}
	if texts := strings.Join(suggestTexts(cli.getTaskGraphSuggestions(docWithCursor("/taskgraph ", 11))), " "); !strings.Contains(texts, " plan run retry ") {
		t.Errorf("completion lacks plan, run, retry: %q", texts)
	}
}

// The model keeps its English tool wording; only the human side changed.
func TestTaskGraphErrorsKeepToolWordingForTheModel(t *testing.T) {
	a := newTestTaskGraphAdapter(t)
	_, err := a.Status("")
	if err == nil || err.Error() != "@taskgraph: no runs yet (create one with plan or run)" || taskGraphHumanText(err) != i18n.T("taskgraph.err.no_runs") {
		t.Fatalf("two wordings of one error: %v / %q", err, taskGraphHumanText(err))
	}
	if _, err := a.Plan("{"); err == nil || !strings.HasPrefix(err.Error(), "@taskgraph plan: ") || !strings.HasPrefix(taskGraphHumanText(err), "/taskgraph plan: ") {
		t.Fatalf("wrapped error: %v", err)
	}
	if out, err := a.Prune("all"); err != nil || out != "pruned 0 run(s), scope: all (active run kept)" {
		t.Fatalf("model-facing prune text changed: %q %v", out, err)
	}
}

func TestChannelCommandFiltersAndGuards(t *testing.T) {
	cli := newTestCLIWithMCP(t)
	ch := cli.mcpManager.Channels()
	for _, m := range [][2]string{{"ci", "build-1"}, {"ops", "disk-full"}, {"ci", "build-2"}} {
		ch.Push(mcp.ChannelMessage{ServerName: "s", Channel: m[0], Content: m[1]})
	}
	run := func(line string) string {
		return captureStdout(t, func() { cli.handleChannelCommand(context.Background(), line) })
	}
	has := func(out string, want ...string) bool {
		return !slices.ContainsFunc(want, func(w string) bool { return !strings.Contains(out, w) })
	}
	if out := run("/channel list ci"); !has(out, "build-1", "build-2") || has(out, "disk-full") {
		t.Errorf("list <channel> must filter: %q", out)
	}
	if out := run("/channel list --limit 1"); !has(out, "build-2") || has(out, "disk-full") {
		t.Errorf("list --limit 1: %q", out)
	}
	if out := run("/channel ops --limit 5"); !has(out, "disk-full") || has(out, "build-1") {
		t.Errorf("bare channel name: %q", out)
	}
	if out := run("/channel"); !has(out, "build-1", "disk-full", "build-2") {
		t.Errorf("bare /channel: %q", out)
	}
	if out := run("/channel unread"); !has(out, i18n.T("chan.cmd.box_title_unread"), "build-1") {
		t.Errorf("unread: %q", out)
	}
	for line, want := range map[string]string{
		"/channel puase":            i18n.T("chan.cmd.unknown_word", "puase"),
		"/channel list --limit":     i18n.T("execparity.flag_needs_value", "--limit"),
		"/channel list --limit x":   i18n.T("chan.cmd.bad_limit", "x"),
		"/channel list --since 1h":  i18n.T("execparity.unknown_flag", "/channel list", "--since"),
		"/channel list ci ops":      i18n.T("execparity.extra_arg", "/channel list", "ops"),
		"/channel clear all":        i18n.T("chan.cmd.clear_bad_arg", "all"),
		"/channel clear --all more": i18n.T("chan.cmd.clear_bad_arg", "more"),
	} {
		if out := run(line); !has(out, want) || has(out, "build-") {
			t.Errorf("%s: want only %q, got %q", line, want, out)
		}
	}
	if ch.Count() != 3 {
		t.Fatalf("a refused clear must not drain the inbox: %d left", ch.Count())
	}
	suggest := func(line string) []string {
		return suggestTexts(cli.getChannelSuggestions(docWithCursor(line, len(line))))
	}
	if got := strings.Join(suggest("/channel list "), " "); got != "--limit ci ops" {
		t.Errorf("list completion: %q", got)
	}
	if got := suggest("/channel un"); len(got) != 1 || got[0] != "unread" {
		t.Errorf("unread completion: %v", got)
	}
	if run("/channel clear --all"); ch.Count() != 0 {
		t.Fatalf("clear --all must drain: %d left", ch.Count())
	}
}

// @channels rules reads the rules from flattened argv; no gate is reachable.
func TestChannelsToolRulesIsReadOnly(t *testing.T) {
	cli := newTestCLIWithMCP(t)
	plugins.SetChannelsAdapter(&channelsPluginAdapter{cli: cli})
	t.Cleanup(func() { plugins.SetChannelsAdapter(nil) })
	tool := plugins.NewBuiltinChannelsPlugin()
	exec := func(argv ...string) (string, error) { return tool.Execute(context.Background(), argv) }
	if out, err := exec("rules"); err != nil || !strings.Contains(out, "No channel trigger rules") {
		t.Fatalf("no rules: %q %v", out, err)
	}
	if err := cli.channelTriggers.engine.SetRules([]triggers.Rule{
		{Name: "deploys", Channel: "ci", Mode: triggers.ModeNotify, ContentRegex: "failed", Tools: []string{"@coder"}},
	}); err != nil {
		t.Fatal(err)
	}
	cli.channelTriggerPause()
	out, err := exec("rules")
	if err != nil || !strings.Contains(out, "- deploys [notify] server=* channel=ci contentRegex=failed tools=@coder") || !strings.Contains(out, "PAUSED") {
		t.Fatalf("rules: %q %v", out, err)
	}
	if !tool.IsReadOnly([]string{"rules"}) {
		t.Error("rules must be read-only")
	}
	for _, argv := range [][]string{
		{"rules", "reload"}, {"rules", "--action", "reload"},
		{"confirm", "--id", "1"}, {"run", "--seq", "1"}, {"clear", "--all"}, {"pause"}, {"resume"},
	} {
		if out, err := exec(argv...); err == nil {
			t.Errorf("%v must be refused, got %q", argv, out)
		}
	}
	if !cli.channelTriggerIsPaused() {
		t.Error("the tool must not be able to resume the trigger engine")
	}
	if !strings.Contains(tool.Usage(), "rules") || !strings.Contains(tool.Schema(), "rules") {
		t.Error("rules must be documented in Usage and Schema")
	}
}

func TestParseMoAOptions(t *testing.T) {
	t.Setenv("CHATCLI_MOA_LAYERS", "")
	t.Setenv("CHATCLI_MOA_PANEL", "")
	o, err := parseMoAOptions("--models a,b:m2 --aggregator=c:x --positions FULL --layers 9 what now?")
	if err != nil || len(o.models) != 2 || o.models[1].Model != "m2" || o.aggregator == nil || o.aggregator.Model != "x" ||
		o.positions != moaPanelFull || o.layers != 4 || o.layersAsked != 9 || o.prompt != "what now?" {
		t.Fatalf("all flags: %+v %v", o, err)
	}
	if o, err := parseMoAOptions("-- --verbose is which flag?"); err != nil || o.prompt != "--verbose is which flag?" || o.layersAsked != 0 {
		t.Fatalf("-- must end the flags: %+v %v", o, err)
	}
	// A dashed prompt is refused, so the refusal and the usage name the escape.
	for _, text := range []string{i18n.T("moa.cmd.flags_usage"), i18n.T("moa.cmd.unknown_flag", "--why")} {
		if !strings.Contains(text, " -- ") {
			t.Errorf("no pointer to the -- escape: %q", text)
		}
	}
	for in, want := range map[string]string{
		"--model a q":            i18n.T("moa.cmd.unknown_flag", "--model"),
		"--models a, b q":        i18n.T("moa.cmd.models_trailing_comma", "a,"),
		"--models":               i18n.T("execparity.flag_needs_value", "--models"),
		"--models --layers 2 q":  i18n.T("execparity.flag_needs_value", "--models"),
		"--models , q":           i18n.T("moa.cmd.models_trailing_comma", ","),
		"--aggregator a,b q":     i18n.T("moa.cmd.bad_aggregator", "a,b"),
		"--positions verbose q":  i18n.T("moa.cmd.bad_positions", "verbose"),
		"--layers 2 --unknown q": i18n.T("moa.cmd.unknown_flag", "--unknown"),
	} {
		if _, err := parseMoAOptions(in); err == nil || err.Error() != want {
			t.Errorf("%q: got %v, want %q", in, err, want)
		}
	}
}

// A worker of a detached run asks only when the agent turn that started it
// is still reading stdin; otherwise "ask" is the unattended denial and stdin
// is never read. The prompt drops type-ahead, so the answer is sent after it.
func TestDetachedTaskGraphRunAsksOnlyALiveTurn(t *testing.T) {
	const tool, args = "@coder", `{"cmd":"exec","args":{"cmd":"curl https://example.invalid"}}`
	denied := "false " + i18n.T("agent.policy.unattended_denied", tool)
	for _, c := range []struct {
		name            string
		byTool, reading bool
		want            string
	}{
		{"human run with a live reader", false, true, denied},
		{"tool run whose turn ended", true, false, denied},
		{"tool run with a live reader", true, true, "true "},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			withStdinInteractive(t, true)
			pa, err := newWorkerPolicyAdapter(zap.NewNop())
			if err != nil || pa.pm.Check(tool, args) != coder.ActionAsk {
				t.Fatalf("the test needs an ask verdict (%v)", err)
			}
			stdin, stale := make(chan string, 1), make(chan string, 1)
			stdin <- "typed before the prompt"
			stale <- "y"
			coder.SetSecurityPromptSalvage(func([]string) { stdin <- "y" })
			defer coder.SetSecurityPromptSalvage(nil)
			pa.setStdinCh(stale) // what a finished turn leaves behind
			live := map[bool]chan string{true: stdin}[c.reading]
			pa.liveStdin = func() <-chan string { return live }
			got := make(chan string, 2)
			var once sync.Once // the reviewer's dispatch would ask again
			a := newTestTaskGraphAdapter(t)
			release := make(chan struct{})
			close(release)
			a.dispatcherFn = func() (taskGraphDispatcher, error) {
				return gatedDispatcher{release: release, policy: func(ctx context.Context) {
					once.Do(func() {
						allowed, reason := pa.CheckAndPrompt(ctx, tool, args)
						got <- fmt.Sprint(allowed, " ", reason)
					})
				}}, nil
			}
			plugins.SetTaskGraphAdapter(a)
			t.Cleanup(func() { plugins.SetTaskGraphAdapter(nil) })
			plan := filepath.Join(t.TempDir(), "plan.json")
			_ = os.WriteFile(plan, []byte(testPlanJSON), 0o600)
			// No captureStdout: the prompt prints from the run's goroutine.
			if !c.byTool {
				(&ChatCLI{taskGraphAdapter: a}).handleTaskGraphCommand(context.Background(), "/taskgraph run "+plan)
			} else if _, err = plugins.NewBuiltinTaskGraphPlugin().Execute(context.Background(), []string{"run", "--background", "--file", plan}); err != nil {
				t.Fatalf("run did not start: %v", err)
			}
			waitTaskGraphBackground(t) // fails if a prompt nobody feeds holds the run
			got <- "the run never asked"
			if v := <-got; v != c.want {
				t.Fatalf("verdict %q, want %q", v, c.want)
			}
			if wantLeft := map[bool]int{true: 0, false: 1}[c.want != denied]; len(stdin) != wantLeft || len(stale) != 1 {
				t.Fatalf("stdin answers left: live %d (want %d), stale %d (want 1)", len(stdin), wantLeft, len(stale))
			}
		})
	}
}

func TestMoACommandPerRunFlagsAndList(t *testing.T) {
	mgr := &moaFakeManager{
		minimalManager: &minimalManager{providers: []string{"a", "agg", "b", "zz"}},
		answers:        map[string]string{"a": "Yes. A.", "b": "No. B.", "zz": "ENV MEMBER", "agg": "SESSION AGG", "agg2": "FLAG AGG"},
	}
	c := newMoaCLI(mgr, "agg", "m")
	ch, err := NewContextHandlerAt(t.TempDir(), zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	c.contextHandler = ch
	t.Setenv("CHATCLI_MOA_MODELS", "zz")
	t.Setenv("CHATCLI_MOA_AGGREGATOR", "")
	t.Setenv("CHATCLI_MOA_PANEL", "full")
	t.Setenv("CHATCLI_MOA_LAYERS", "")
	run := func(line string) (string, error) {
		var err error
		out := captureStdout(t, func() { err = c.runMoACommand(context.Background(), line) })
		return out, err
	}
	out, err := run("/moa --models a,b --aggregator agg2 --positions off --layers 9 migrate?")
	if err != nil || !strings.Contains(out, "FLAG AGG") || !strings.Contains(out, i18n.T("moa.cmd.layers_clamped", 9, 4, 4)) {
		t.Fatalf("flags must win over the env and the session: %v\n%s", err, out)
	}
	if strings.Contains(out, "ENV MEMBER") || strings.Contains(out, "Yes. A.") {
		t.Fatalf("--models must replace the env panel and --positions off must hide the positions:\n%s", out)
	}
	if out, err := run("/moa list"); err != nil || !strings.Contains(out, i18n.T("moa.tool.list.header")) || len(c.history) != 2 {
		t.Fatalf("/moa list must list and ask nothing: %v %q (history %d)", err, out, len(c.history))
	}
	if out, err := run("/moa --model a q"); err == nil || !strings.Contains(out, i18n.T("moa.cmd.flags_usage")) || len(c.history) != 2 {
		t.Fatalf("an unknown flag must not reach the panel: %v %q", err, out)
	}
	// The tool, from the flattened argv: one comma string is two members.
	plugins.SetMoaAdapter(&moaPluginAdapter{cli: c})
	t.Cleanup(func() { plugins.SetMoaAdapter(nil) })
	out, err = plugins.NewBuiltinMoaPlugin().Execute(context.Background(), []string{"ask", "--models", "a,b", "--prompt", "migrate?"})
	if err != nil || !strings.Contains(out, i18n.T("moa.tool.synthesized", "a, b", "agg")) {
		t.Fatalf(`models "a,b" must be two members: %v %q`, err, out)
	}
	suggest := func(line string) string {
		return strings.Join(suggestTexts(c.getMoASuggestions(docWithCursor(line, len(line)))), " ")
	}
	for line, want := range map[string]string{
		"/moa ":                         "--layers --models --aggregator --positions list",
		"/moa --positions ":             "lead full off",
		"/moa --models ":                "a agg b zz",
		"/moa --models a,b --agg":       "--aggregator",
		"/moa --models a,b what about ": "",
		"/moa list ":                    "",
	} {
		if got := suggest(line); got != want {
			t.Errorf("completion of %q: %q, want %q", line, got, want)
		}
	}
}

// The graph description names what the engine reads.
func TestTaskGraphSchemaDocumentsToolsAndPromptAliases(t *testing.T) {
	schema := plugins.NewBuiltinTaskGraphPlugin().Schema()
	for _, want := range []string{`\"tools\"?:[`, `aliases \"task\", \"description\"`} {
		if !strings.Contains(schema, want) {
			t.Errorf("schema lacks %s", want)
		}
	}
	g, err := taskgraph.ParseGraph(`{"name":"x","tasks":[{"id":"T1","task":"from alias","tools":["@websearch"]}]}`)
	if err != nil || g.Tasks[0].Prompt != "from alias" || len(g.Tasks[0].Tools) != 1 {
		t.Fatalf("the documented fields must be the ones the engine reads: %+v %v", g, err)
	}
}
