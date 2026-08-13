/*
 * ChatCLI - Slash command chat→coder auto-route (post-unwind consumer)
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * A command whose resolved mode is coder cannot run as a chat turn: chat is
 * tool-less by design and the model refuses, pointing the user at /coder.
 * maybeAutorouteCoderCommand (commands_integration.go) intercepts those
 * invocations at the REPL dispatch and unwinds out of go-prompt exactly like
 * a manual /coder; this file owns what happens AFTER the unwind — expand the
 * template, run the coder ReAct loop one-shot, and hand the prompt back to
 * chat when the loop reaches its final answer.
 */
package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/diillson/chatcli/cli/agent"
	"github.com/diillson/chatcli/i18n"
)

// runPendingCoderCommand consumes pendingCoderCommandInput (staged by the
// auto-route before the panic-unwind) and executes it as a coder one-shot.
// Expansion happens HERE, not at dispatch: the terminal is back in cooked
// mode so pre-exec "!" approval prompts can read stdin, and the staged
// model/effort/allowed-tools hints land immediately before the
// AgentMode.Run that consumes them.
func (cli *ChatCLI) runPendingCoderCommand(ctx context.Context) {
	input := cli.pendingCoderCommandInput
	cli.pendingCoderCommandInput = ""

	expanded, ok := cli.expandSlashCommandInput(ctx, input, !cli.unattended)
	if !ok {
		// Catalog changed between dispatch and consumption (reload, file
		// removed): report and return to the REPL instead of guessing.
		fmt.Println(colorize(i18n.T("commands.autoroute.expand_failed", input), ColorYellow))
		return
	}

	wd, _ := os.Getwd()
	renderer := agent.NewUIRenderer(cli.logger)
	// Objective shows the invocation the user typed ("/deploy prod"), never
	// the expanded template — the body can be hundreds of lines.
	renderer.RenderModeBanner("🛠️", i18n.T("coder.banner.title"), agent.ColorCyan, [][2]string{
		{i18n.T("coder.banner.objective"), input},
		{i18n.T("coder.banner.workspace"), wd},
		{i18n.T("coder.banner.policy"), i18n.T("commands.autoroute.banner_mode")},
	})

	cli.runWithCancellation(ctx, "Coder Mode", func(c context.Context) error {
		return cli.runCoderQuery(c, expanded, false)
	})

	// runCoderQuery sets isOneShot=true and Run deliberately never resets
	// it; the REPL default is interactive, so restore it or the next manual
	// /coder would inherit one-shot exits.
	if cli.agentMode != nil {
		cli.agentMode.isOneShot = false
	}
	if cli.memWorker != nil {
		cli.memWorker.nudge(ctx)
	}
	fmt.Println(colorize("\n "+i18n.T("commands.autoroute.returned_to_chat"), ColorGreen))
}
