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

	"github.com/diillson/chatcli/cli/plugins"
	"github.com/diillson/chatcli/cli/scheduler"
	"go.uber.org/zap"
)

// jobsTagged lists the jobs of the scheduler that carry the proc-owner tag.
func jobsTagged(cli *ChatCLI) map[string]string {
	out := map[string]string{}
	for _, sum := range cli.scheduler.List(scheduler.ListFilter{IncludeTerminal: true}) {
		if j, err := cli.scheduler.Query(sum.ID); err == nil {
			if owner, ok := j.Tags[procOwnerJobTag]; ok {
				out[j.Name] = owner
			}
		}
	}
	return out
}

// The proc-owner tag decides whose process table a job acts on when it
// fires. It is set from the creating context only: no spelling of "tags"
// in a tool input — the decoder is case-insensitive — and no --tag on the
// command line can carry a "chatcli." tag in, whoever the caller is. The
// call is refused and no job is created.
func TestReservedJobTagsCannotBeSetFromInput(t *testing.T) {
	cli := newCLIWithScheduler(t)
	cli.logger = zap.NewNop()
	cli.commandHandler = NewCommandHandler(cli)
	adapter := &schedulerPluginAdapter{cli: cli}
	plugins.SetSchedulerAdapter(adapter)
	t.Cleanup(func() { plugins.SetSchedulerAdapter(nil) })
	owner := plugins.SchedulerOwner{Kind: "user", ID: "t"}
	terminal := context.Background()
	alice := WithProcSession(terminal, "alice")

	forged := map[string]string{
		"tags exact":      `{"name":"f1","when":"+1h","do":"/proc list","tags":{"chatcli.proc_owner":"session:victim"}}`,
		"Tags mixed case": `{"name":"f2","when":"+1h","do":"/proc list","Tags":{"chatcli.proc_owner":"session:victim"}}`,
		"TAGS upper":      `{"name":"f3","when":"+1h","do":"/proc list","TAGS":{"chatcli.proc_owner":"session:victim"}}`,
		"other chatcli.*": `{"name":"f4","when":"+1h","do":"/proc list","tags":{"chatcli.anything":"x"}}`,
		"prefix any case": `{"name":"f5","when":"+1h","do":"/proc list","tags":{"ChatCLI.proc_owner":"session:victim"}}`,
		"wait with a tag": `{"name":"f6","until":"file:/nonexistent/never","Tags":{"chatcli.proc_owner":"session:victim"},"async":true}`,
		"padded tag key":  `{"name":"f7","when":"+1h","do":"/proc list","tags":{" chatcli.proc_owner":"session:victim"}}`,
		"from a session":  `{"name":"f8","when":"+1h","do":"/proc list","Tags":{"chatcli.proc_owner":"session:victim"}}`,
	}
	for name, input := range forged {
		ctx := terminal
		if name == "from a session" {
			ctx = alice
		}
		call := adapter.ScheduleJob
		if strings.Contains(input, `"until"`) {
			call = adapter.WaitUntil
		}
		out, err := call(ctx, owner, input)
		if err == nil && !strings.Contains(out, "reserved") {
			t.Errorf("%s: a reserved tag in the input must be refused: %q", name, out)
		}
	}
	// The flattened envelope of the tool, with the mixed-case key.
	tool := plugins.NewBuiltinSchedulerPlugin()
	for _, argv := range [][]string{
		{"schedule", "--Tags", `{"chatcli.proc_owner":"session:victim"}`, "--do", "/proc list", "--name", "f9", "--when", "+1h"},
		{`{"cmd":"schedule","args":{"name":"f10","when":"+1h","do":"/proc list","Tags":{"chatcli.proc_owner":"session:victim"}}}`},
	} {
		if out, err := tool.Execute(terminal, argv); err == nil && !strings.Contains(out, "reserved") {
			t.Errorf("%v: must be refused: %s", argv, out)
		}
	}
	// The human's --tag.
	out := captureStdout(t, func() {
		cli.handleScheduleCommand(terminal, `/schedule f11 --when +1h --do "/proc list" --tag chatcli.proc_owner=session:victim`)
	})
	if !strings.Contains(out, "reserved") {
		t.Errorf("/schedule --tag with a reserved tag must be refused: %q", out)
	}
	if got := jobsTagged(cli); len(got) != 0 {
		t.Fatalf("a forged owner reached a job: %v", got)
	}
	if n := len(cli.scheduler.List(scheduler.ListFilter{IncludeTerminal: true})); n != 0 {
		t.Fatalf("a refused input created %d job(s)", n)
	}

	// The creating context is what sets it — through the tool, /schedule
	// and /wait alike — and other tags pass through.
	if out, err := adapter.ScheduleJob(alice, owner, `{"name":"ok1","when":"+1h","do":"/proc list","tags":{"team":"x"}}`); err != nil || !strings.Contains(out, `"ok": true`) {
		t.Fatalf("a session's job: %q %v", out, err)
	}
	if out, err := adapter.ScheduleJob(terminal, owner, `{"name":"ok2","when":"+1h","do":"/proc list"}`); err != nil || !strings.Contains(out, `"ok": true`) {
		t.Fatalf("the terminal's job: %s %v", out, err)
	}
	_ = captureStdout(t, func() {
		cli.handleScheduleCommand(alice, `/schedule ok3 --when +1h --do "/proc list" --tag team=y`)
	})
	if got := jobsTagged(cli); len(got) != 2 || got["ok1"] != "session:alice" || got["ok3"] != "session:alice" {
		t.Fatalf("jobs created by alice carry alice's table, the terminal's none: %v", got)
	}
}
