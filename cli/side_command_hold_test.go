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

	"github.com/diillson/chatcli/cli/scheduler"
	"github.com/diillson/chatcli/i18n"
)

// A command typed while a turn runs executes inside the turn. A blocking
// wait there used to hold the turn itself in the one-shot loops, where the
// "a turn is executing" flag is never set; the side-command path now marks
// itself and the hold is declined.
func TestSideCommandNeverHoldsThePrompt(t *testing.T) {
	c, ready := newWaitCLI(t)
	c.commandHandler = NewCommandHandler(c)
	a := &AgentMode{cli: c}

	_ = captureStdout(t, func() {
		c.handleWaitCommand(context.Background(), "/wait file:"+ready+" --name held --timeout 5m")
	})
	id, msg := c.resolveJobRef(context.Background(), "held")
	if msg != "" {
		t.Fatal(msg)
	}
	if c.isExecuting.Load() {
		t.Fatal("precondition: this test is about a loop that never sets isExecuting")
	}

	done := make(chan string, 1)
	go func() {
		done <- captureStdout(t, func() { a.execSideCommand(context.Background(), "/jobs wait held") })
	}()
	var out string
	select {
	case out = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("/jobs wait typed mid-turn held the turn")
	}
	if !strings.Contains(out, i18n.T("scheduler.wait.block_mid_turn")) || !strings.Contains(out, string(id)) {
		t.Fatalf("the mid-turn wait did not report and return:\n%s", out)
	}
	if c.sideCommandDepth.Load() != 0 {
		t.Fatal("the side-command mark outlived the command")
	}
	if j, _ := c.scheduler.Query(id); j == nil || j.Status.IsTerminal() {
		t.Fatalf("the job did not keep running: %+v", j)
	}

	// Outside a turn the same command may hold, and reports no reason not to.
	if why := c.holdUnavailable(); why != "" {
		t.Fatalf("the prompt outside a turn refuses to hold: %s", why)
	}
	_ = c.scheduler.Cancel(id, "test done", scheduler.Owner{Kind: scheduler.OwnerUser, ID: "t"})
}
