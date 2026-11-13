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

	"github.com/diillson/chatcli/i18n"
)

// The operator mark belongs to the command the person typed, for as long
// as its handler runs — not to what that command starts. /resume typed at
// the prompt runs an agent turn: a command issued inside that turn, and a
// background run the turn leaves behind, are not the operator.
func TestOperatorMarkCoversOnlyTheTypedCommand(t *testing.T) {
	var cli *ChatCLI
	var turnCtx context.Context
	var midTurn string
	cli = resumeCLI(t, func(ctx context.Context) error {
		turnCtx = ctx
		midTurn = captureStdout(t, func() { cli.commandHandler.HandleCommand(ctx, "/proc list --all") })
		return nil
	})
	cli.commandHandler = NewCommandHandler(cli)
	t.Cleanup(cli.shutdownProcSupervisor)
	token := firedPark(t, cli)

	cli.runInterruptibleCommand("/resume " + token)
	if turnCtx == nil {
		t.Fatal("the typed /resume must have started a turn")
	}
	refused := i18n.T("leftovers.proc.operator_only")
	if !strings.Contains(midTurn, refused) {
		t.Errorf("a command issued inside the turn a prompt command started was taken for the operator: %q", midTurn)
	}
	// A background run keeps its context's values after the turn is over.
	detached := context.WithoutCancel(turnCtx)
	if out := captureStdout(t, func() { cli.commandHandler.HandleCommand(detached, "/proc list --all") }); !strings.Contains(out, refused) {
		t.Errorf("a run left behind by that turn was taken for the operator: %q", out)
	}
	// The typed command itself is still the operator's.
	if out := captureStdout(t, func() { cli.runInterruptibleCommand("/proc list --all") }); strings.Contains(out, refused) {
		t.Errorf("the prompt command must keep working: %q", out)
	}
}
