/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"context"
	"fmt"

	"github.com/diillson/chatcli/i18n"
)

// Slash commands run synchronously inside the prompt's executor, and the
// process-wide signal handler treats Ctrl+C as "quit" unless something
// interruptible is running. A command that takes long — a compaction, an
// OAuth login waiting on the browser, a blocking wait — therefore could
// not be interrupted: the first Ctrl+C began shutting the session down.
//
// The REPL now registers each command here for as long as its handler
// runs. The first Ctrl+C cancels the command's context and keeps the
// session; a handler that ignores its context is not left without a way
// out, because the registration is spent after one interrupt and the next
// Ctrl+C quits as before.

// commandInterrupt is the registration of the slash command in flight.
type commandInterrupt struct {
	cancel context.CancelFunc
	spent  bool // already interrupted once
}

// beginInterruptible registers the command about to run and returns the
// context Ctrl+C cancels. release ends the registration WITHOUT cancelling
// that context: handlers hand it to work that outlives them (a turn started
// in the background, a watcher), which must keep running exactly as it did
// when commands ran on context.Background(). Nested calls share the outer
// registration.
func (cli *ChatCLI) beginInterruptible(ctx context.Context) (context.Context, func()) {
	cli.mu.Lock()
	defer cli.mu.Unlock()
	if cli.cmdInterrupt != nil {
		return ctx, func() {}
	}
	cmdCtx, cancel := context.WithCancel(ctx)
	reg := &commandInterrupt{cancel: cancel}
	cli.cmdInterrupt = reg
	return cmdCtx, func() {
		cli.mu.Lock()
		if cli.cmdInterrupt == reg {
			cli.cmdInterrupt = nil
		}
		cli.mu.Unlock()
	}
}

// CanInterrupt reports whether Ctrl+C has something to cancel: a running
// agent/chat operation, or a slash command not yet interrupted. The signal
// handler quits the application when it returns false.
func (cli *ChatCLI) CanInterrupt() bool {
	if cli.isExecuting.Load() {
		return true
	}
	cli.mu.Lock()
	defer cli.mu.Unlock()
	return cli.cmdInterrupt != nil && !cli.cmdInterrupt.spent
}

// interruptCommandLocked cancels the slash command in flight, once. The caller
// holds cli.mu.
func (cli *ChatCLI) interruptCommandLocked() {
	reg := cli.cmdInterrupt
	if reg == nil || reg.spent {
		return
	}
	reg.spent = true
	reg.cancel()
	fmt.Println(colorize("\n  "+i18n.T("cli.signal.command_interrupted"), ColorYellow))
}

// runInterruptibleCommand runs one slash command from the prompt with
// Ctrl+C wired to its context. The registration is released on every way
// out, including the panic sentinels commands use to switch modes.
func (cli *ChatCLI) runInterruptibleCommand(line string) bool {
	ctx, release := cli.beginInterruptible(context.Background())
	defer release()
	return cli.commandHandler.HandleCommand(ctx, line)
}
