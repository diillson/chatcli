//go:build !windows

/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"context"
	"github.com/diillson/chatcli/i18n"
	"go.uber.org/zap"
	"strings"
	"testing"
	"time"
)

// A mistyped /proc line must be reported, never guessed into another verb.
func TestProcCommandParseRejects(t *testing.T) {
	for _, line := range []string{
		"/proc restart p1", "/proc stop", "/proc logs", "/proc stop p1 --tail 5", "/proc logs p1 --since 5",
		"/proc logs p1 --tail", "/proc logs p1 --tail many", "/proc logs p1 --tail 0", "/proc logs p1 --grep (",
		"/proc logs p1 --grep --tail", "/proc status p1 p2", "/proc list p1",
	} {
		if _, err := parseProcCommand(line); err == nil {
			t.Errorf("%q must be rejected", line)
		}
	}
	got, err := parseProcCommand("/proc logs p1 --grep err --tail 7")
	if err != nil || got.sub != "logs" || got.id != "p1" || got.tail != 7 || got.grep == nil {
		t.Fatalf("valid logs line: %+v %v", got, err)
	}
}

// The human sees and stops, through /proc, the very process the tool
// started: one registry for both sides.
func TestProcCommandSeesAndStopsWhatTheToolStarted(t *testing.T) {
	cli := &ChatCLI{logger: zap.NewNop()}
	t.Cleanup(cli.shutdownProcSupervisor)
	run := func(line string) string {
		return captureStdout(t, func() { cli.handleProcCommand(context.Background(), line) })
	}
	if out := run("/proc"); !strings.Contains(out, i18n.T("proc.cmd.none_tracked")) {
		t.Fatalf("empty list: %q", out)
	}
	a := &procToolAdapter{cli: cli}
	if _, err := a.Start(`sh -c 'echo ready; echo oops; sleep 30'`, ""); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := a.Wait(context.Background(), "p1", "oops", 10); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	for line, want := range map[string]string{
		"/proc list":                 "p1",
		"/proc status p1":            "pid",
		"/proc logs p1":              "ready",
		"/proc logs p1 --tail 1":     "oops",
		"/proc logs p1 --grep READY": i18n.T("proc.cmd.grep_header", 1, 1, 2),
		"/proc status p9":            i18n.T("proc.cmd.unknown_id", "p9"),
		"/proc bogus":                i18n.T("proc.cmd.usage"),
	} {
		if out := run(line); !strings.Contains(out, want) {
			t.Errorf("%s: want %q in %q", line, want, out)
		}
	}
	if out := run("/proc logs p1 --tail 1"); strings.Contains(out, "\n  ready") {
		t.Errorf("--tail 1 must drop the earlier line: %q", out)
	}
	complete := func(line string) []string {
		return suggestTexts(cli.getProcSuggestions(docWithCursor(line, len(line))))
	}
	if got := strings.Join(complete("/proc "), " "); got != "list status logs stop" {
		t.Errorf("subcommands: %q", got)
	}
	if got := complete("/proc stop "); len(got) != 1 || got[0] != "p1" {
		t.Errorf("live ids: %v", got)
	}
	if got := strings.Join(complete("/proc logs p1 "), " "); got != "--grep --tail" {
		t.Errorf("logs flags: %q", got)
	}
	// stop only requests the stop; the supervisor terminates on its own.
	if out := run("/proc stop p1"); !strings.Contains(out, i18n.T("proc.cmd.stopping", "p1", "p1")) {
		t.Fatalf("stop must answer at once that it is stopping: %q", out)
	}
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if info, err := a.supervisor().Status("p1"); err != nil || time.Now().After(deadline) {
			t.Fatalf("the tool's registry must show p1 stopped: %+v %v", info, err)
		} else if info.State != "running" {
			break
		}
	}
	if out := run("/proc stop p1"); strings.Contains(out, i18n.T("proc.cmd.stopping", "p1", "p1")) {
		t.Errorf("stopping an exited process must just show it: %q", out)
	}
	if out := run("/proc status p1"); !strings.Contains(out, "p1") || strings.Contains(out, "pid") {
		t.Errorf("status after stop must be the exited line: %q", out)
	}
}

// /proc is routed, runnable mid-run and on headless surfaces.
func TestProcCommandIsRegisteredEverywhere(t *testing.T) {
	cli := &ChatCLI{logger: zap.NewNop()}
	_, routed := NewCommandHandler(cli).lookup("/proc list")
	_, completed := cli.routeSlashPrefix("/proc ", docWithCursor("/proc ", 6))
	if !routed || !completed || !acpCommandAllow["/proc"] || !isSideCommand("/proc stop p1") {
		t.Errorf("/proc: routed=%v completer=%v; it must also be allowed over ACP and mid-run", routed, completed)
	}
}
