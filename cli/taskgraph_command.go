/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * /taskgraph — human view over @taskgraph runs: status, per-task detail,
 * run listing and cancel. Allowlisted as a mid-run side command so the user
 * can watch a live graph without touching the orchestrator loop. The
 * technical state lines reuse the taskgraph renderers on purpose: human and
 * model read the same ground truth.
 */
package cli

import (
	"fmt"
	"strings"

	"github.com/diillson/chatcli/i18n"
)

// handleTaskGraphCommand routes /taskgraph subcommands.
func (cli *ChatCLI) handleTaskGraphCommand(userInput string) {
	adapter := cli.taskGraphAdapter
	if adapter == nil {
		fmt.Println(colorize("  "+i18n.T("taskgraph.unavailable"), ColorYellow))
		return
	}
	args := strings.Fields(strings.TrimSpace(userInput))
	sub := "status"
	if len(args) >= 2 {
		sub = strings.ToLower(args[1])
	}
	var rest []string
	if len(args) > 2 {
		rest = args[2:]
	}
	arg := ""
	if len(rest) > 0 {
		arg = rest[0]
	}

	var (
		out string
		err error
	)
	switch sub {
	case "status", "st":
		out, err = adapter.Status(arg)
	case "show", "task":
		if arg == "" {
			fmt.Println(colorize("  "+i18n.T("taskgraph.task.missing"), ColorYellow))
			cli.printTaskGraphUsage()
			return
		}
		runID := ""
		if len(rest) > 1 {
			runID = rest[1]
		}
		out, err = adapter.Show(runID, arg)
	case "list", "ls", "runs":
		out, err = adapter.List()
	case "dash", "dashboard", "ui":
		out, err = adapter.Dash(arg)
	case "cancel", "stop":
		out, err = adapter.Cancel()
	case "help":
		cli.printTaskGraphUsage()
		return
	default:
		// Bare "/taskgraph tg-..." reads as status of that run.
		if strings.HasPrefix(sub, "tg-") {
			out, err = adapter.Status(sub)
			break
		}
		fmt.Println(colorize("  "+i18n.T("taskgraph.subcommand.unknown", sub), ColorYellow))
		cli.printTaskGraphUsage()
		return
	}
	if err != nil {
		fmt.Println(colorize("  ❌ "+err.Error(), ColorRed))
		return
	}
	for _, line := range strings.Split(out, "\n") {
		fmt.Println("  " + line)
	}
}

// printTaskGraphUsage prints the /taskgraph help block.
func (cli *ChatCLI) printTaskGraphUsage() {
	fmt.Println(colorize("  "+i18n.T("taskgraph.usage.title"), ColorCyan))
	fmt.Println(colorize("  "+i18n.T("taskgraph.usage.body"), ColorGray))
}
