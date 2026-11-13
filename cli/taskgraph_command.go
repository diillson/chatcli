/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * /taskgraph — the human side of @taskgraph: plan, run and retry a graph
 * from a plan file, and status, per-task detail, run listing, dashboard,
 * prune and cancel. Allowlisted as a mid-run side command and on headless
 * surfaces, so NOTHING here may hold its caller: run and retry start the
 * graph in the tool's own background slot and return the run id. The
 * technical state lines reuse the taskgraph renderers on purpose: human and
 * model read the same ground truth.
 */
package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/diillson/chatcli/cli/plugins"
	"github.com/diillson/chatcli/i18n"
)

// handleTaskGraphCommand routes /taskgraph subcommands.
func (cli *ChatCLI) handleTaskGraphCommand(ctx context.Context, userInput string) {
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
		out, err = taskGraphWithBackground(adapter, arg)(adapter.Status(arg))
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
		out, err = taskGraphWithBackground(adapter, runID)(adapter.Show(runID, arg))
	case "list", "ls", "runs":
		out, err = adapter.List()
	case "plan", "run", "retry", "dash", "dashboard", "ui", "prune", "gc", "cancel", "stop":
		out, err = cli.taskGraphAct(ctx, adapter, sub, rest)
	case "help":
		cli.printTaskGraphUsage()
		return
	default:
		// Bare "/taskgraph tg-..." reads as status of that run.
		if strings.HasPrefix(sub, "tg-") {
			out, err = taskGraphWithBackground(adapter, sub)(adapter.Status(sub))
			break
		}
		fmt.Println(colorize("  "+i18n.T("taskgraph.subcommand.unknown", sub), ColorYellow))
		cli.printTaskGraphUsage()
		return
	}
	if err != nil {
		fmt.Println(colorize("  ❌ "+taskGraphHumanText(err), ColorRed))
		return
	}
	for _, line := range strings.Split(out, "\n") {
		fmt.Println("  " + line)
	}
}

// taskGraphWithBackground appends the background run's state to a
// status/show answer about THAT run. An error stays an error and an empty
// id is resolved first: passed through empty it matched any finished run,
// so a wrong task id came back as a success printing that run's report.
func taskGraphWithBackground(adapter *taskGraphAdapter, runID string) func(string, error) (string, error) {
	return func(out string, err error) (string, error) {
		if err != nil {
			return out, err
		}
		id, rerr := adapter.ResolveRunID(runID, "")
		if rerr != nil {
			return out, nil
		}
		return plugins.TaskGraphWithBackground(out, nil, id)
	}
}

// taskGraphAct runs the subcommands that change something, worded here
// for a person (the adapter's own strings address the model).
func (cli *ChatCLI) taskGraphAct(ctx context.Context, adapter *taskGraphAdapter, sub string, rest []string) (string, error) {
	arg := ""
	if len(rest) > 0 {
		arg = rest[0]
	}
	switch sub {
	case "plan":
		graphJSON, err := taskGraphPlanFileArg(sub, rest)
		if err != nil {
			return "", err
		}
		runID, tasks, err := adapter.planRun(graphJSON)
		if err != nil {
			return "", err
		}
		return i18n.T("taskgraph.cmd.plan_accepted", runID, tasks, runID), nil
	case "run":
		return cli.taskGraphRun(ctx, adapter, rest)
	case "retry":
		return cli.taskGraphRetry(ctx, adapter, rest)
	case "prune", "gc":
		n, retention, err := adapter.pruneRuns(arg)
		if err != nil {
			return "", err
		}
		if retention <= 0 {
			return i18n.T("taskgraph.cmd.pruned_all", n), nil
		}
		return i18n.T("taskgraph.cmd.pruned", n, retention.String()), nil
	case "cancel", "stop":
		// The background slot first: a detached run is only reachable
		// through it. Requested, not awaited (stdin reader, capture slot).
		bgID, hadBG := plugins.CancelTaskGraphBackgroundRun()
		_, err := adapter.Cancel()
		switch {
		case hadBG:
			return i18n.T("taskgraph.cmd.background_canceled", bgID), nil
		case err != nil:
			return "", err
		}
		return i18n.T("taskgraph.cmd.cancel_requested"), nil
	default: // dash
		dashURL, err := adapter.dashURL(arg)
		if err != nil {
			return "", err
		}
		return i18n.T("taskgraph.cmd.dash", dashURL), nil
	}
}

// taskGraphPlanFileArg loads the plan file a plan/run subcommand names.
func taskGraphPlanFileArg(sub string, rest []string) (string, error) {
	if len(rest) != 1 {
		return "", errors.New(i18n.T("taskgraph.cmd.file_required", sub))
	}
	graphJSON, err := plugins.LoadTaskGraphPlanFile(rest[0])
	if err != nil {
		return "", errors.New(i18n.T("taskgraph.cmd.file_unreadable", rest[0], strings.TrimPrefix(err.Error(), "@taskgraph: ")))
	}
	return graphJSON, nil
}

// taskGraphRun starts "/taskgraph run <file.json|run-id>" detached. No
// implicit "latest": that would spend money on a plan the user did not name.
func (cli *ChatCLI) taskGraphRun(ctx context.Context, adapter *taskGraphAdapter, rest []string) (string, error) {
	if len(rest) == 1 && strings.HasPrefix(rest[0], "tg-") {
		if _, statErr := os.Stat(rest[0]); statErr != nil {
			return cli.taskGraphStartBackground(ctx, adapter, rest[0], "")
		}
	}
	graphJSON, err := taskGraphPlanFileArg("run", rest)
	if err != nil {
		return "", err
	}
	return cli.taskGraphStartBackground(ctx, adapter, "", graphJSON)
}

// taskGraphRetry handles "retry [run-id] [task-id]": the "tg-" token is the
// run (latest when absent), the other the task (the only failed one).
func (cli *ChatCLI) taskGraphRetry(ctx context.Context, adapter *taskGraphAdapter, rest []string) (string, error) {
	runID, taskID := "", ""
	for _, tok := range rest {
		switch {
		case strings.HasPrefix(tok, "tg-") && runID == "":
			runID = tok
		case taskID == "":
			taskID = tok
		default:
			return "", errors.New(i18n.T("execparity.extra_arg", "/taskgraph retry", tok))
		}
	}
	if _, err := adapter.dispatcher(); err != nil {
		return "", err
	}
	if active := plugins.TaskGraphBackgroundActiveID(); active != "" {
		return "", errors.New(i18n.T("taskgraph.cmd.background_busy", active))
	}
	store, _, taskID, err := adapter.reopenFailedTask(runID, taskID)
	if err != nil {
		return "", err
	}
	out, err := cli.taskGraphStartBackground(ctx, adapter, store.RunID(), "")
	if err != nil {
		return "", err
	}
	return i18n.T("taskgraph.cmd.retry_reopened", taskID) + "\n" + out, nil
}

// taskGraphStartBackground starts the run in the tool's background slot,
// checking the dispatcher first: detached, its absence only shows in status.
func (cli *ChatCLI) taskGraphStartBackground(ctx context.Context, adapter *taskGraphAdapter, runID, graphJSON string) (string, error) {
	if _, err := adapter.dispatcher(); err != nil {
		return "", err
	}
	// A foreground tool run owns the session's one run: launched anyway, the
	// new run reported "started", died at once and left a run directory.
	if active := plugins.TaskGraphBackgroundActiveID(); active != "" {
		return "", errors.New(i18n.T("taskgraph.cmd.background_busy", active))
	}
	if adapter.activeRunID() != "" {
		return "", errTaskGraphBusy()
	}
	id, err := plugins.StartTaskGraphBackgroundRun(plugins.TaskGraphNoTurn(ctx), adapter, runID, graphJSON)
	if err != nil {
		return "", err
	}
	return i18n.T("taskgraph.cmd.run_started", id, id), nil
}

// printTaskGraphUsage prints the /taskgraph help block.
func (cli *ChatCLI) printTaskGraphUsage() {
	fmt.Println(colorize("  "+i18n.T("taskgraph.usage.title"), ColorCyan))
	fmt.Println(colorize("  "+i18n.T("taskgraph.usage.body"), ColorGray))
}
