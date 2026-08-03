/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * /agents — human view over the live agent run registry.
 *
 * Shows which agent executions are alive right now (orchestrator, workers,
 * subagents, MoA members, scheduler headless runs) as a parent/child tree
 * with per-run turn/action progress, plus the recent finished history.
 * Mirrors what the @agents tool exposes to the LLM, rendered for people.
 */
package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/diillson/chatcli/cli/agent/runs"
	"github.com/diillson/chatcli/i18n"
)

// agentRunsRegistry resolves the run registry; a package-level indirection
// so tests can point the command layer at an isolated registry.
var agentRunsRegistry = runs.Default

// agentsCommandRecentN caps how many finished runs /agents list shows.
const agentsCommandRecentN = 10

// handleAgentsCommand routes /agents subcommands.
func (cli *ChatCLI) handleAgentsCommand(userInput string) {
	args := strings.Fields(strings.TrimSpace(userInput))
	sub := "list"
	if len(args) >= 2 {
		sub = args[1]
	}

	switch sub {
	case "list", "ls":
		cli.agentsList()
	case "show", "info":
		if len(args) < 3 {
			fmt.Println(colorize("  "+i18n.T("agents.id.missing"), ColorYellow))
			cli.printAgentsUsage()
			return
		}
		cli.agentsShow(args[2])
	case "cancel", "stop":
		if len(args) < 3 {
			fmt.Println(colorize("  "+i18n.T("agents.id.missing"), ColorYellow))
			cli.printAgentsUsage()
			return
		}
		cli.agentsCancel(args[2])
	case "help", "-h", "--help":
		cli.printAgentsUsage()
	default:
		fmt.Println(colorize("  "+i18n.T("agents.unknown", sub), ColorYellow))
		cli.printAgentsUsage()
	}
}

// agentsList renders active runs as a tree, then the recent history.
func (cli *ChatCLI) agentsList() {
	reg := agentRunsRegistry()
	active := reg.Active()
	recent := reg.Recent(agentsCommandRecentN)

	if len(active) == 0 && len(recent) == 0 {
		fmt.Println(colorize("  "+i18n.T("agents.list.empty"), ColorGray))
		return
	}

	fmt.Println(colorize(fmt.Sprintf("  %s (%d)", i18n.T("agents.list.active"), len(active)), ColorCyan+ColorBold))
	if len(active) == 0 {
		fmt.Println(colorize("    "+i18n.T("agents.list.none"), ColorGray))
	} else {
		byParent := make(map[string][]runs.Info, len(active))
		liveIDs := make(map[string]bool, len(active))
		for _, info := range active {
			liveIDs[info.ID] = true
		}
		var roots []runs.Info
		for _, info := range active {
			if info.ParentID != "" && liveIDs[info.ParentID] {
				byParent[info.ParentID] = append(byParent[info.ParentID], info)
			} else {
				roots = append(roots, info)
			}
		}
		for _, root := range roots {
			cli.printAgentRunTree(root, byParent, 0)
		}
	}

	fmt.Println()
	fmt.Println(colorize(fmt.Sprintf("  %s (%d)", i18n.T("agents.list.recent"), len(recent)), ColorCyan+ColorBold))
	if len(recent) == 0 {
		fmt.Println(colorize("    "+i18n.T("agents.list.none"), ColorGray))
	}
	for _, info := range recent {
		fmt.Println(cli.formatAgentRunHuman(info, 0))
	}

	cli.printRemoteAgentRuns()
}

// printRemoteAgentRuns appends the runs mirrored by other chatcli processes
// (gateway daemon, scheduler) when the hub runs mirror is active.
func (cli *ChatCLI) printRemoteAgentRuns() {
	remote := listRemoteAgentRuns()
	if len(remote) == 0 {
		return
	}
	fmt.Println()
	fmt.Println(colorize(fmt.Sprintf("  %s (%d)", i18n.T("agents.list.remote"), len(remote)), ColorCyan+ColorBold))
	for _, r := range remote {
		fmt.Println(cli.formatRemoteAgentRunHuman(r))
	}
}

// formatRemoteAgentRunHuman renders one mirrored run, tagging the owning
// process and flagging heartbeat silence.
func (cli *ChatCLI) formatRemoteAgentRunHuman(r remoteAgentRun) string {
	line := cli.formatAgentRunHuman(r.Info, 0)
	origin := r.Info.Origin
	if origin == "" {
		origin = r.Info.Instance
	}
	line += colorize(" ["+origin+"]", ColorPurple)
	if r.Stale {
		line += colorize(" ⚠ "+i18n.T("agents.remote.stale"), ColorYellow)
	}
	return line
}

// findRemoteAgentRun looks a mirrored run up by ID.
func findRemoteAgentRun(id string) (remoteAgentRun, bool) {
	for _, r := range listRemoteAgentRuns() {
		if r.Info.ID == id {
			return r, true
		}
	}
	return remoteAgentRun{}, false
}

// printAgentRunTree prints one active run and recurses into its children.
func (cli *ChatCLI) printAgentRunTree(info runs.Info, byParent map[string][]runs.Info, depth int) {
	fmt.Println(cli.formatAgentRunHuman(info, depth))
	for _, child := range byParent[info.ID] {
		cli.printAgentRunTree(child, byParent, depth+1)
	}
}

// formatAgentRunHuman renders one run line for terminal display.
func (cli *ChatCLI) formatAgentRunHuman(info runs.Info, depth int) string {
	indent := strings.Repeat("  ", depth+2)
	branch := ""
	if depth > 0 {
		branch = "↳ "
	}

	var icon, color string
	switch info.Status {
	case runs.StatusRunning:
		icon, color = "▶", ColorCyan
	case runs.StatusCompleted:
		icon, color = "✓", ColorGreen
	case runs.StatusCancelled:
		icon, color = "⊘", ColorYellow
	default:
		icon, color = "✗", ColorRed
	}

	var parts []string
	parts = append(parts, fmt.Sprintf("%s [%s/%s]", info.ID, info.Kind, info.Agent))
	if info.Status == runs.StatusRunning {
		if info.Turn > 0 && info.MaxTurns > 0 {
			parts = append(parts, i18n.T("agents.turn", info.Turn, info.MaxTurns))
		}
		if info.Action != "" {
			parts = append(parts, truncateForUI(info.Action, 40))
		}
	} else {
		parts = append(parts, i18n.T("agents.status."+string(info.Status)))
		if info.Err != "" && info.Status == runs.StatusFailed {
			parts = append(parts, truncateForUI(info.Err, 60))
		}
	}
	parts = append(parts, info.Elapsed().Round(time.Second).String())

	line := fmt.Sprintf("%s%s%s %s", indent, branch, colorize(icon, color), strings.Join(parts, " · "))
	task := truncateForUI(info.Task, 70)
	if task != "" {
		line += colorize(" — "+task, ColorGray)
	}
	return line
}

// agentsShow prints the detail of one run by ID.
func (cli *ChatCLI) agentsShow(id string) {
	reg := agentRunsRegistry()
	info, ok := reg.Get(id)
	if !ok {
		if r, found := findRemoteAgentRun(id); found {
			fmt.Println(cli.formatRemoteAgentRunHuman(r))
			fmt.Printf("      %s %s\n", colorize(i18n.T("agents.field.instance")+":", ColorGray), r.Info.Instance)
			if r.Info.Origin != "" {
				fmt.Printf("      %s %s\n", colorize(i18n.T("agents.field.origin")+":", ColorGray), r.Info.Origin)
			}
			return
		}
		fmt.Println(colorize("  "+i18n.T("agents.show.notfound", id), ColorYellow))
		return
	}
	fmt.Println(cli.formatAgentRunHuman(info, 0))
	kvLine := func(label, value string) {
		fmt.Printf("      %s %s\n", colorize(label+":", ColorGray), value)
	}
	if info.ParentID != "" {
		kvLine(i18n.T("agents.field.parent"), info.ParentID)
	}
	if info.Origin != "" {
		kvLine(i18n.T("agents.field.origin"), info.Origin)
	}
	kvLine(i18n.T("agents.field.toolcalls"), fmt.Sprintf("%d", info.ToolCalls))
	kvLine(i18n.T("agents.field.started"), info.StartedAt.Format("15:04:05"))
	if info.Err != "" {
		kvLine(i18n.T("agents.field.error"), info.Err)
	}
	if children := reg.Children(info.ID); len(children) > 0 {
		fmt.Println(colorize(fmt.Sprintf("      %s (%d):", i18n.T("agents.field.children"), len(children)), ColorGray))
		for _, child := range children {
			fmt.Println(cli.formatAgentRunHuman(child, 1))
		}
	}
}

// agentsCancel requests cancellation of a live run by ID — locally when this
// process owns it, otherwise by flagging the mirrored row so the owning
// process cancels on its next sync tick.
func (cli *ChatCLI) agentsCancel(id string) {
	reg := agentRunsRegistry()
	if reg.Cancel(id) {
		fmt.Println(colorize("  "+i18n.T("agents.cancel.requested", id), ColorGreen))
		return
	}
	if _, ok := reg.Get(id); ok {
		fmt.Println(colorize("  "+i18n.T("agents.cancel.finished", id), ColorYellow))
		return
	}
	if r, found := findRemoteAgentRun(id); found {
		if !r.Info.Status.Terminal() && requestRemoteAgentRunCancel(id) {
			fmt.Println(colorize("  "+i18n.T("agents.cancel.remote", id), ColorGreen))
			return
		}
		fmt.Println(colorize("  "+i18n.T("agents.cancel.finished", id), ColorYellow))
		return
	}
	fmt.Println(colorize("  "+i18n.T("agents.show.notfound", id), ColorYellow))
}

// printAgentsUsage prints /agents usage help.
func (cli *ChatCLI) printAgentsUsage() {
	fmt.Println(colorize("  "+i18n.T("agents.usage"), ColorGray))
}
