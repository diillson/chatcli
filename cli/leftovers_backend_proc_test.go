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

	"github.com/diillson/chatcli/cli/agent/proc"
	"github.com/diillson/chatcli/cli/plugins"
	"github.com/diillson/chatcli/i18n"
	"go.uber.org/zap"
)

// Two sessions served by one chatcli process: through the tool and through
// /proc each sees and stops only what it started, another session's id is
// answered exactly like an id that does not exist, and the terminal keeps
// its own table.
func TestProcToolAndCommandAreScopedPerSession(t *testing.T) {
	cli := &ChatCLI{logger: zap.NewNop(), manager: &loopFakeManager{}}
	cli.commandHandler = NewCommandHandler(cli)
	plugins.SetProcAdapter(&procToolAdapter{cli: cli})
	t.Cleanup(func() {
		plugins.SetProcAdapter(nil)
		cli.shutdownProcSupervisor()
	})
	p := plugins.NewBuiltinProcPlugin()
	terminal := context.Background()
	alice, bob := WithProcSession(terminal, "alice"), WithProcSession(terminal, "bob")
	tool := func(ctx context.Context, argv ...string) string {
		out, err := p.Execute(ctx, argv)
		if err != nil {
			return "ERR " + err.Error()
		}
		return out
	}
	slash := func(ctx context.Context, line string) string {
		out, err := cli.RunSlashCommandRPC(ctx, line)
		if err != nil {
			t.Fatalf("%s: %v", line, err)
		}
		return out
	}

	// The flattened {"cmd":"start","args":{"command":...}} of each session.
	if a, b := tool(alice, "start", "--command", "sh -c 'echo alice-ready; sleep 30'"), tool(bob, "start", "--command", "sleep 30"); !strings.HasPrefix(a, "started p1 ") || !strings.HasPrefix(b, "started p2 ") {
		t.Fatalf("starts: %q %q", a, b)
	}
	if out := tool(alice, "wait", "--id", "p1", "--pattern", "alice-ready", "--timeout", "30"); !strings.Contains(out, "Output matched") {
		t.Fatalf("alice wait: %q", out)
	}
	// A background run keeps its session after the turn's context ends.
	for name, c := range map[string]struct {
		ctx       context.Context
		has, lack string
	}{
		"alice": {alice, "p1 running", "p2"}, "bob": {bob, "p2 running", "p1"},
		"terminal": {terminal, "No background processes tracked", "running"},
		"detached": {context.WithoutCancel(alice), "p1 running", "p2"},
	} {
		if out := tool(c.ctx, "list"); !strings.Contains(out, c.has) || strings.Contains(out, c.lack) {
			t.Errorf("%s's list: %q", name, out)
		}
	}
	if a, term := slash(alice, "/proc list"), slash(terminal, "/proc"); !strings.Contains(a, "p1") || strings.Contains(a, "p2") || !strings.Contains(term, i18n.T("proc.cmd.none_tracked")) {
		t.Errorf("/proc must list the caller's table: alice %q, terminal %q", a, term)
	}

	// Bob on alice's id: tool and command answer as for an unknown id.
	for _, argv := range []string{
		"status --id %s", "logs --id %s", "logs --grep x --id %s", "wait --id %s --timeout 1",
		"stdin --id %s --text x", "stop --id %s", "remove --id %s",
	} {
		with := func(id string) string { return tool(bob, strings.Fields(strings.ReplaceAll(argv, "%s", id))...) }
		unowned, unknown := with("p1"), strings.ReplaceAll(with("p999"), "p999", "p1")
		if !strings.HasPrefix(unowned, "ERR ") || unowned != unknown {
			t.Errorf("%s on another session's id:\n got  %q\n want %q", argv, unowned, unknown)
		}
	}
	for _, sub := range []string{"status", "logs", "stop"} {
		unowned := slash(bob, "/proc "+sub+" p1")
		unknown := strings.ReplaceAll(slash(bob, "/proc "+sub+" p999"), "p999", "p1")
		if unowned != unknown || !strings.Contains(unowned, i18n.T("proc.cmd.unknown_id", "p1")) {
			t.Errorf("/proc %s on another session's id: got %q, want %q", sub, unowned, unknown)
		}
	}
	if out := tool(alice, "status", "--id", "p1"); !strings.Contains(out, "p1 running") {
		t.Fatalf("bob's calls must not have touched alice's process: %q", out)
	}

	// Each stops its own.
	if out := slash(bob, "/proc stop p2"); !strings.Contains(out, i18n.T("proc.cmd.stopping", "p2", "p2")) {
		t.Errorf("bob stops his own: %q", out)
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
