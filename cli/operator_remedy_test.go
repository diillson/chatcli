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
	"github.com/diillson/chatcli/i18n"
)

// A session id is whatever the client sent: it may hold spaces, and it may
// hold terminal escapes. The operator addresses an owner by the short
// handle /proc list --all prints (or by its quoted name), the name reaches
// the operator's terminal and the telemetry stripped of control
// characters, and status, logs and remove work across owners like stop.
func TestOperatorAddressesAnyOwnerByHandle(t *testing.T) {
	cli, tool := procTestCLI(t)
	terminal := context.Background()
	hostile := WithProcSession(terminal, "my \x1b[31mred\x07 session\r\n")
	owner := proc.OwnerFrom(hostile)
	if strings.ContainsAny(owner, "\x1b\x07\r\n") || owner != "session:my [31mred session" {
		t.Fatalf("an owner must carry no control characters into the table and the telemetry: %q", owner)
	}
	if a, b := tool(hostile, "start", "--command", "sh -c 'echo hostile-line; sleep 61'"), tool(WithProcSession(terminal, "plain"), "start", "--command", "sleep 62"); !strings.HasPrefix(a, "started p1 ") || !strings.HasPrefix(b, "started p1 ") {
		t.Fatalf("starts: %q %q", a, b)
	}
	if out := tool(hostile, "wait", "--id", "p1", "--pattern", "hostile-line", "--timeout", "30"); !strings.Contains(out, "Output matched") {
		t.Fatalf("wait: %q", out)
	}
	prompt := func(line string) string {
		return captureStdout(t, func() { cli.runInterruptibleCommand(line) })
	}

	all := prompt("/proc list --all")
	if strings.ContainsAny(all, "\x07\r") || strings.Contains(all, "\x1b[31m") {
		t.Fatalf("the listing wrote a client's escape sequence to the operator's terminal: %q", all)
	}
	for _, want := range []string{"[o1 session:my [31mred session]", "sleep 61", "[o2 session:plain]", "sleep 62"} {
		if !strings.Contains(all, want) {
			t.Fatalf("/proc list --all must show each owner with its handle (%q missing): %q", want, all)
		}
	}

	// By handle, and by the quoted name, across the verbs.
	if out := prompt("/proc status p1 --owner o1"); !strings.Contains(out, "sleep 61") {
		t.Errorf("status by handle: %q", out)
	}
	if out := prompt(`/proc status p1 --owner "session:my [31mred session"`); !strings.Contains(out, "sleep 61") {
		t.Errorf("status by quoted owner: %q", out)
	}
	if out := prompt("/proc logs p1 --owner o1 --tail 5"); !strings.Contains(out, "hostile-line") {
		t.Errorf("logs by handle: %q", out)
	}
	if out := prompt("/proc logs p1 --owner o2 --grep hostile"); strings.Contains(out, "hostile-line") {
		t.Errorf("logs of the other owner must not be this one's: %q", out)
	}
	if out := prompt("/proc remove p1 --owner o1"); !strings.Contains(out, i18n.T("leftovers.proc.remove_running", "p1")) {
		t.Errorf("a running process is not removed: %q", out)
	}
	for _, line := range []string{"/proc stop p1 --owner o9", `/proc stop p1 --owner "no such owner"`, "/proc status p1 --owner", `/proc status p1 --owner "unclosed`} {
		if out := prompt(line); strings.Contains(out, i18n.T("proc.cmd.stopping", "p1", "p1")) || strings.Contains(out, "sleep 6") {
			t.Errorf("%s must act on nothing: %q", line, out)
		}
	}
	if out := prompt("/proc stop p1 --owner o1"); !strings.Contains(out, i18n.T("proc.cmd.stopping", "p1", "p1")) {
		t.Fatalf("stop by handle: %q", out)
	}
	for deadline := time.Now().Add(30 * time.Second); strings.Contains(tool(hostile, "status", "--id", "p1"), " running "); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the process was never stopped")
		}
	}
	if out := prompt("/proc remove p1 --owner o1"); !strings.Contains(out, i18n.T("leftovers.proc.removed", "p1")) {
		t.Fatalf("remove by handle: %q", out)
	}
	if out := tool(hostile, "list"); !strings.HasPrefix(out, "No background processes tracked") {
		t.Fatalf("the removed entry is gone from its owner's table: %q", out)
	}
	if out := tool(WithProcSession(terminal, "plain"), "status", "--id", "p1"); !strings.Contains(out, " running ") {
		t.Fatalf("the other owner's p1 must be untouched: %q", out)
	}

	// A session cannot use any of it, and the terminal's own remove works.
	for _, line := range []string{"/proc status p1 --owner o2", "/proc logs p1 --owner o2", "/proc remove p1 --owner o2"} {
		if out, _ := cli.RunSlashCommandRPC(hostile, line); !strings.Contains(out, i18n.T("leftovers.proc.operator_only")) {
			t.Errorf("%s from a session must be refused: %q", line, out)
		}
	}
	complete := func(line string) string {
		return strings.Join(suggestTexts(cli.getProcSuggestions(docWithCursor(line, len(line)))), " ")
	}
	if got := complete("/proc "); got != "list status logs stop remove" {
		t.Errorf("subcommands: %q", got)
	}
	if got := complete("/proc logs p1 "); got != "--grep --tail --owner" {
		t.Errorf("logs flags: %q", got)
	}
	// Only owners that still hold a process are offered: o1's is gone.
	if got := complete("/proc remove p1 --owner "); got != "o2" {
		t.Errorf("owner handles: %q", got)
	}
}
