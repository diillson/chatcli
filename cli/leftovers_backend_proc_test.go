//go:build !windows

/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/diillson/chatcli/cli/agent/proc"
	"github.com/diillson/chatcli/cli/plugins"
	"github.com/diillson/chatcli/cli/scheduler"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/pkg/pulse"
	"go.uber.org/zap"
)

// procTestCLI is a ChatCLI with the @proc tool and /proc wired, and a
// helper that runs the tool from flattened argv under a context.
func procTestCLI(t *testing.T) (*ChatCLI, func(ctx context.Context, argv ...string) string) {
	t.Helper()
	cli := &ChatCLI{logger: zap.NewNop(), manager: &loopFakeManager{}}
	cli.commandHandler = NewCommandHandler(cli)
	plugins.SetProcAdapter(&procToolAdapter{cli: cli})
	t.Cleanup(func() {
		plugins.SetProcAdapter(nil)
		cli.shutdownProcSupervisor()
	})
	p := plugins.NewBuiltinProcPlugin()
	return cli, func(ctx context.Context, argv ...string) string {
		out, err := p.Execute(ctx, argv)
		if err != nil {
			return "ERR " + err.Error()
		}
		return out
	}
}

// Two sessions served by one chatcli process: through the tool and through
// /proc each sees and stops only what it started, an id it does not hold
// is answered exactly like one that does not exist, the same id in two
// tables is two processes, and the terminal keeps its own table.
func TestProcToolAndCommandAreScopedPerSession(t *testing.T) {
	cli, tool := procTestCLI(t)
	terminal := context.Background()
	alice, bob := WithProcSession(terminal, "alice"), WithProcSession(terminal, "bob")
	slash := func(ctx context.Context, line string) string {
		out, err := cli.RunSlashCommandRPC(ctx, line)
		if err != nil {
			t.Fatalf("%s: %v", line, err)
		}
		return out
	}

	// The flattened {"cmd":"start","args":{"command":...}} of each session.
	// Ids are each session's own: both get a p1.
	a1 := tool(alice, "start", "--command", "sh -c 'echo alice-ready; sleep 30'")
	a2 := tool(alice, "start", "--command", "sleep 31")
	b1 := tool(bob, "start", "--command", "sleep 32")
	if !strings.HasPrefix(a1, "started p1 ") || !strings.HasPrefix(a2, "started p2 ") || !strings.HasPrefix(b1, "started p1 ") {
		t.Fatalf("starts: %q %q %q", a1, a2, b1)
	}
	if out := tool(alice, "wait", "--id", "p1", "--pattern", "alice-ready", "--timeout", "30"); !strings.Contains(out, "Output matched") {
		t.Fatalf("alice wait: %q", out)
	}
	// A background run keeps its session after the turn's context ends.
	for name, c := range map[string]struct {
		ctx       context.Context
		has, lack string
	}{
		"alice": {alice, "sleep 31", "sleep 32"}, "bob": {bob, "sleep 32", "sleep 31"},
		"terminal": {terminal, "No background processes tracked", "sleep 3"},
		"detached": {context.WithoutCancel(alice), "sleep 31", "sleep 32"},
	} {
		if out := tool(c.ctx, "list"); !strings.Contains(out, c.has) || strings.Contains(out, c.lack) {
			t.Errorf("%s's list: %q", name, out)
		}
	}
	if a, term := slash(alice, "/proc list"), slash(terminal, "/proc"); !strings.Contains(a, "sleep 31") || strings.Contains(a, "sleep 32") || !strings.Contains(term, i18n.T("proc.cmd.none_tracked")) {
		t.Errorf("/proc must list the caller's table: alice %q, terminal %q", a, term)
	}

	// Bob on p2, an id only alice holds: tool and command answer as for an
	// id nobody holds.
	for _, argv := range []string{
		"status --id %s", "logs --id %s", "logs --grep x --id %s", "wait --id %s --timeout 1",
		"stdin --id %s --text x", "stop --id %s", "remove --id %s",
	} {
		with := func(id string) string { return tool(bob, strings.Fields(strings.ReplaceAll(argv, "%s", id))...) }
		unowned, unknown := with("p2"), strings.ReplaceAll(with("p999"), "p999", "p2")
		if !strings.HasPrefix(unowned, "ERR ") || unowned != unknown {
			t.Errorf("%s on another session's id:\n got  %q\n want %q", argv, unowned, unknown)
		}
	}
	for _, sub := range []string{"status", "logs", "stop"} {
		unowned := slash(bob, "/proc "+sub+" p2")
		unknown := strings.ReplaceAll(slash(bob, "/proc "+sub+" p999"), "p999", "p2")
		if unowned != unknown || !strings.Contains(unowned, i18n.T("proc.cmd.unknown_id", "p2")) {
			t.Errorf("/proc %s on another session's id: got %q, want %q", sub, unowned, unknown)
		}
	}

	// Bob stops HIS p1; alice's p1 and p2 never notice.
	if out := slash(bob, "/proc stop p1"); !strings.Contains(out, i18n.T("proc.cmd.stopping", "p1", "p1")) {
		t.Errorf("bob stops his own: %q", out)
	}
	if out := tool(bob, "stop", "--id", "p1"); !strings.HasPrefix(out, "stopped p1") {
		t.Errorf("bob's p1 must stop: %q", out)
	}
	if out := tool(alice, "list"); strings.Count(out, " running ") != 2 {
		t.Fatalf("bob's stop of his p1 must not touch alice's: %q", out)
	}
	if out := tool(alice, "stop", "--id", "p1") + "; " + tool(alice, "remove", "--id", "p1"); !strings.HasPrefix(out, "stopped p1") || !strings.HasSuffix(out, "; removed p1") {
		t.Errorf("alice stops and removes her own: %q", out)
	}
}

// A run started over RPC carries its session to every tool it calls, a run
// without one stays on the shared table, and an isolated gateway principal
// gets a table no RPC session of the same name can reach.
func TestRunsCarryTheirProcOwner(t *testing.T) {
	c := &ChatCLI{logger: zap.NewNop(), manager: &loopFakeManager{}}
	seen := map[string]string{}
	for _, session := range []string{"s1", ""} {
		if _, err := c.runLoopRPC(context.Background(), RPCRunOpts{Session: session}, func(ctx context.Context) error {
			seen[session] = proc.OwnerFrom(ctx)
			return nil
		}); err != nil {
			t.Fatalf("runLoopRPC: %v", err)
		}
	}
	if seen["s1"] != procSessionOwner("s1") || seen[""] != "" {
		t.Fatalf("owners seen by the run: %v", seen)
	}
	ctx := context.Background()
	if c.withTenantProcOwner(ctx) != ctx {
		t.Fatal("without an active tenant the context must stay on the shared table")
	}
	c.tenants = &tenantPool{active: "alice"}
	if got := proc.OwnerFrom(c.withTenantProcOwner(ctx)); got != procTenantOwner("alice") || got == proc.OwnerFrom(WithProcSession(ctx, "alice")) {
		t.Fatalf("tenant owner %q must differ from the RPC session of the same name", got)
	}
}

// The person at this terminal can see and stop every owner's processes —
// the remedy when sessions hold the host's capacity. Nobody else can: not
// a session, not a headless caller, not a command issued mid-turn, not a
// scheduled job, not the tool.
func TestOnlyTheTerminalPromptSeesEveryOwner(t *testing.T) {
	cli, tool := procTestCLI(t)
	terminal := context.Background()
	alice := WithProcSession(terminal, "alice")
	if a, term := tool(alice, "start", "--command", "sleep 41"), tool(terminal, "start", "--command", "sleep 42"); !strings.HasPrefix(a, "started p1 ") || !strings.HasPrefix(term, "started p1 ") {
		t.Fatalf("starts: %q %q", a, term)
	}
	prompt := func(line string) string {
		return captureStdout(t, func() { cli.runInterruptibleCommand(line) })
	}

	all := prompt("/proc list --all")
	for _, want := range []string{"[session:alice]", "sleep 41", "[" + procTerminalOwner + "]", "sleep 42"} {
		if !strings.Contains(all, want) {
			t.Fatalf("/proc list --all must show every owner's process with its owner (%q missing): %q", want, all)
		}
	}
	if plain := prompt("/proc list"); strings.Contains(plain, "sleep 41") || !strings.Contains(plain, "sleep 42") {
		t.Fatalf("without --all the prompt still lists only the terminal's: %q", plain)
	}

	// Every other way in is refused, and nothing is listed or stopped.
	refused := i18n.T("leftovers.proc.operator_only")
	bridge := &schedulerBridge{cli: cli}
	for name, run := range map[string]func(line string) string{
		"a session over RPC": func(line string) string { out, _ := cli.RunSlashCommandRPC(alice, line); return out },
		"a headless caller":  func(line string) string { out, _ := cli.RunSlashCommandRPC(terminal, line); return out },
		"a command issued mid-turn": func(line string) string {
			return captureStdout(t, func() { cli.commandHandler.HandleCommand(terminal, line) })
		},
		"a scheduled job": func(line string) string { out, _, _ := bridge.ExecuteSlashCommand(terminal, line, false); return out },
		"a prompt marker under a session": func(line string) string {
			return captureStdout(t, func() { cli.commandHandler.HandleCommand(withLocalOperator(alice), line) })
		},
	} {
		for _, line := range []string{"/proc list --all", "/proc stop p1 --owner session:alice", "/proc stop p1 --owner terminal"} {
			if out := run(line); !strings.Contains(out, refused) || strings.Contains(out, "sleep 4") {
				t.Errorf("%s, %s: must be refused, got %q", name, line, out)
			}
		}
	}
	for _, argv := range [][]string{{"list", "--all"}, {"stop", "--id", "p1", "--owner", "session:alice"}} {
		if out := tool(terminal, argv...); !strings.HasPrefix(out, "ERR ") || !strings.Contains(out, "unknown argument") {
			t.Errorf("the tool must not know %v: %q", argv, out)
		}
	}
	if out := tool(alice, "list") + tool(terminal, "list"); strings.Count(out, " running ") != 2 {
		t.Fatalf("a refused stop touched a process: %q", out)
	}

	// What is not understood is reported and does nothing.
	for _, line := range []string{"/proc stop p1 --owner", "/proc status p1 --all", "/proc list --owner session:alice", "/proc stop p1 --all", "/proc stop p9 --owner session:alice", "/proc stop p1 --owner session:nobody"} {
		if out := prompt(line); strings.Contains(out, i18n.T("proc.cmd.stopping", "p1", "p1")) {
			t.Errorf("%s must not stop anything: %q", line, out)
		}
	}

	// The operator stops the session's process; the terminal's own p1 —
	// the same id — is untouched.
	if out := prompt("/proc stop p1 --owner session:alice"); !strings.Contains(out, i18n.T("proc.cmd.stopping", "p1", "p1")) {
		t.Fatalf("the operator's stop: %q", out)
	}
	for deadline := time.Now().Add(30 * time.Second); strings.Contains(tool(alice, "status", "--id", "p1"), " running "); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("alice's process was never stopped")
		}
	}
	if out := tool(terminal, "status", "--id", "p1"); !strings.Contains(out, " running ") {
		t.Fatalf("the terminal's own p1 must be untouched: %q", out)
	}

	// The flags are offered at the prompt, with the owners that exist.
	complete := func(line string) string {
		return strings.Join(suggestTexts(cli.getProcSuggestions(docWithCursor(line, len(line)))), " ")
	}
	if got := complete("/proc list "); got != "--all" {
		t.Errorf("list flags: %q", got)
	}
	if got := complete("/proc stop p1 "); got != "--owner" {
		t.Errorf("stop flags: %q", got)
	}
	if got := complete("/proc stop p1 --owner "); got != "session:alice terminal" {
		t.Errorf("owners: %q", got)
	}
}

// A job is created under its session's context and fires under the
// scheduler's. The session's process table travels on the job: a scheduled
// /proc acts on the table of the session that scheduled it, and neither a
// model nor a --tag can aim a job at someone else's.
func TestScheduledJobActsOnItsSessionTable(t *testing.T) {
	cli, tool := procTestCLI(t)
	terminal := context.Background()
	alice := WithProcSession(terminal, "alice")
	tool(alice, "start", "--command", "sleep 51")
	tool(terminal, "start", "--command", "sleep 52")

	forged := map[string]string{procOwnerJobTag: "session:bob", "team": "x"}
	if got := stampProcOwner(alice, forged); got[procOwnerJobTag] != "session:alice" || got["team"] != "x" {
		t.Fatalf("the creating session must be recorded over a forged value: %v", got)
	}
	if got := stampProcOwner(terminal, map[string]string{procOwnerJobTag: "session:bob"}); got[procOwnerJobTag] != "" {
		t.Fatalf("a job created at the terminal must not keep a forged owner: %v", got)
	}
	if got := stampProcOwner(alice, nil); got[procOwnerJobTag] != "session:alice" {
		t.Fatalf("a job with no tags still gets its owner: %v", got)
	}
	in := `{"name":"n","when":"+1m","do":"/proc list","tags":{"chatcli.proc_owner":"session:bob"}}`
	if got := stampProcOwnerJSON(alice, in); !strings.Contains(got, `"chatcli.proc_owner":"session:alice"`) || strings.Contains(got, "session:bob") {
		t.Fatalf("tool input: %s", got)
	}
	if got := stampProcOwnerJSON(terminal, in); strings.Contains(got, "chatcli.proc_owner") {
		t.Fatalf("tool input at the terminal: %s", got)
	}
	if got := stampProcOwnerJSON(alice, "not json"); got != "not json" {
		t.Fatalf("unparsable input must pass through: %s", got)
	}

	bridge := &schedulerBridge{cli: cli}
	fire := func(tags map[string]string) string {
		out, _, err := bridge.ExecuteSlashCommand(scheduler.WithJobTags(terminal, tags), "/proc list", false)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	if out := fire(stampProcOwner(alice, nil)); !strings.Contains(out, "sleep 51") || strings.Contains(out, "sleep 52") {
		t.Fatalf("a job alice scheduled must list alice's processes: %q", out)
	}
	if out := fire(nil); !strings.Contains(out, "sleep 52") || strings.Contains(out, "sleep 51") {
		t.Fatalf("a job scheduled at the terminal lists the terminal's: %q", out)
	}
}

// The @dash tool read by a session shows the background processes that
// session started and nobody else's; the local operator sees them all.
func TestDashToolShowsASessionOnlyItsOwnProcesses(t *testing.T) {
	cliObj, a := newDashToolCLI(t, true)
	procEvent := func(owner, program string) pulse.Event {
		ev := pulse.Event{Kind: pulse.KindBackground, Phase: pulse.PhaseStart, ID: "proc:" + owner + "/p1", Name: "@proc", Status: pulse.StatusRunning}
		ev = ev.With("proc", "p1").With("program", program)
		if owner != "" {
			ev = ev.With(proc.PulseOwnerAttr, owner)
		}
		return ev
	}
	pulse.Emit(procEvent("session:alice", "alice-prog"))
	pulse.Emit(procEvent("session:bob", "bob-prog"))
	pulse.Emit(procEvent("", "term-prog"))
	pulse.Emit(pulse.Event{Kind: pulse.KindTool, Phase: pulse.PhaseStart, ID: "t1", Name: "@shell", Status: pulse.StatusRunning})
	waitSpooled(t, cliObj, func(ev pulse.Event) bool { return ev.ID == "t1" })

	terminal := context.Background()
	alice := WithProcSession(terminal, "alice")
	for name, read := range map[string]func(ctx context.Context) string{
		"events": func(ctx context.Context) string {
			out, err := a.Events(ctx, plugins.DashEventsQuery{})
			if err != nil {
				t.Fatal(err)
			}
			return out
		},
		"summary": func(ctx context.Context) string {
			out, err := a.Summary(ctx, false)
			if err != nil {
				t.Fatal(err)
			}
			return out
		},
	} {
		mine, everything := read(alice), read(terminal)
		if strings.Contains(mine, "bob") || strings.Contains(mine, "term-prog") || strings.Contains(mine, "proc:/p1") {
			t.Errorf("%s read by alice shows another owner's process: %q", name, mine)
		}
		if !strings.Contains(mine, "@shell") {
			t.Errorf("%s read by alice lost what is not a process: %q", name, mine)
		}
		if name == "events" && (!strings.Contains(mine, "alice-prog") || !strings.Contains(everything, "bob-prog") || !strings.Contains(everything, "term-prog")) {
			t.Errorf("alice sees her own, the operator all: alice %q, operator %q", mine, everything)
		}
	}
}
