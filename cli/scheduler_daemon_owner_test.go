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

	"github.com/diillson/chatcli/cli/scheduler"
)

// With a scheduler daemon the job is built on the other side of a socket,
// where the creating context does not reach. The session's process table
// still has to: it crosses in a field of its own that only chatcli's code
// fills, and the daemon still refuses the reserved tag in the input.
func TestProcOwnerReachesAJobCreatedThroughTheDaemon(t *testing.T) {
	a, daemon := newRemoteAdapter(t)
	terminal := context.Background()
	alice := WithProcSession(terminal, "alice")
	tagOf := func(name string) (string, bool) {
		j, ok := daemon.FindByName(name)
		if !ok {
			return "", false
		}
		return j.Tags[procOwnerJobTag], true
	}

	if out, err := a.ScheduleJob(alice, remoteUser, `{"name":"by-alice","when":"+1h","do":"/proc list","tags":{"team":"x"}}`); err != nil || !decodeToolOut(t, out).OK {
		t.Fatalf("schedule from a session: %s %v", out, err)
	}
	if owner, ok := tagOf("by-alice"); !ok || owner != "session:alice" {
		t.Fatalf("the daemon's job must carry the creating session's table, got %q (found %v)", owner, ok)
	}
	if out, err := a.WaitUntil(alice, remoteUser, `{"name":"wait-by-alice","until":"file:/nonexistent/never","async":true}`); err != nil || !decodeToolOut(t, out).OK {
		t.Fatalf("wait from a session: %s %v", out, err)
	}
	if owner, _ := tagOf("wait-by-alice"); owner != "session:alice" {
		t.Fatalf("a wait created through the daemon must carry it too, got %q", owner)
	}
	if out, err := a.ScheduleJob(terminal, remoteUser, `{"name":"by-terminal","when":"+1h","do":"/proc list"}`); err != nil || !decodeToolOut(t, out).OK {
		t.Fatalf("schedule from the terminal: %s %v", out, err)
	}
	if owner, ok := tagOf("by-terminal"); !ok || owner != "" {
		t.Fatalf("a job from the terminal carries no owner, got %q", owner)
	}

	// The input still cannot set it, in any spelling, from any context.
	for _, ctx := range []context.Context{terminal, alice} {
		out, err := a.ScheduleJob(ctx, remoteUser, `{"name":"forged","when":"+1h","do":"/proc list","Tags":{"chatcli.proc_owner":"session:victim"}}`)
		if err == nil && (decodeToolOut(t, out).OK || !strings.Contains(out, "reserved")) {
			t.Errorf("a reserved tag in the input must be refused by the daemon: %s", out)
		}
	}
	if _, ok := tagOf("forged"); ok {
		t.Fatal("a refused input created a job in the daemon")
	}
	for _, sum := range daemon.List(scheduler.ListFilter{IncludeTerminal: true}) {
		if j, err := daemon.Query(sum.ID); err == nil && j.Tags[procOwnerJobTag] == "session:victim" {
			t.Fatalf("job %s carries a forged owner", j.Name)
		}
	}
}
