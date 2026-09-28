/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * web_support.go
 *
 * Slash-command surface of the web UI. The page runs everything the ACP
 * allowlist runs, plus the commands below. They are kept apart from
 * acpCommandAllow on purpose instead of widening it:
 *
 *   - /clear, /retry, /rewind and /plan act on the page's own live
 *     session (its transcript, its bound saved session, its last plan).
 *     The web server serves them itself against that session; over ACP the
 *     editor owns the conversation UI and these have no equivalent there.
 *   - /jobs, /schedule, /hooks and /lsp print headless, but their browser
 *     semantics are narrower than the terminal's: /jobs only reads, and a
 *     job scheduled from the page runs in the web process's scheduler.
 *     Those rules are the page's, so they live on the web path only.
 */
package cli

import (
	"context"
	"strings"

	"github.com/diillson/chatcli/cli/palette"
	"github.com/diillson/chatcli/i18n"
)

// webSessionCommands are served by the web server against the page's live
// session; they never reach the REPL CommandHandler. Their descriptions
// are the page's own, because the semantics are the browser's.
var webSessionCommands = []string{"clear", "retry", "rewind", "plan"}

// webSessionHints are the session commands that take arguments.
var webSessionHints = map[string]bool{"rewind": true, "plan": true}

// webHeadlessCommands run through RunWebCommandRPC with the terminal's own
// handlers, under the web rules below.
var webHeadlessCommands = map[string]bool{"/jobs": true, "/schedule": true, "/hooks": true, "/lsp": true}

// webJobsReadOnly are the /jobs subcommands the page runs: the ones that
// only read. Cancelling, pausing, pruning or driving the daemon stay in the
// terminal, where the scheduler's owner is.
var webJobsReadOnly = map[string]bool{"": true, "list": true, "show": true, "tree": true, "logs": true, "history": true, "help": true}

// ListWebCommands returns the commands the web page advertises: the ACP
// surface plus the web-only ones, each with a localized description.
func (cli *ChatCLI) ListWebCommands() []ACPCommandInfo {
	out := cli.ListACPCommands()
	for _, name := range webSessionCommands {
		info := ACPCommandInfo{Name: name, Description: i18n.T("web.command." + name + ".desc")}
		if webSessionHints[name] {
			info.InputHint = i18n.T("web.command." + name + ".hint")
		}
		out = append(out, info)
	}
	for _, rc := range palette.AllRootCommands() {
		if webHeadlessCommands[rc.Name] {
			out = append(out, ACPCommandInfo{Name: rc.Name[1:], Description: rc.Summary(), InputHint: cli.acpCommandHint(rc.Name)})
		}
	}
	return out
}

// RunWebCommandRPC runs one slash command for the web page: the headless
// path RunSlashCommandRPC gives ACP, with the page's rules for the
// scheduler commands applied first.
func (cli *ChatCLI) RunWebCommandRPC(ctx context.Context, line string) (string, error) {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return cli.RunSlashCommandRPC(ctx, line)
	}
	sub := ""
	if len(fields) > 1 {
		sub = strings.ToLower(fields[1])
	}
	switch fields[0] {
	case "/jobs":
		if !webJobsReadOnly[sub] {
			return i18n.T("web.command.jobs.terminal_only", sub), nil
		}
	case "/schedule":
		// The terminal has no "/schedule list" (it would try to create a
		// job named "list"); in the page it is the listing it reads as.
		if sub == "list" {
			return cli.RunSlashCommandRPC(ctx, "/jobs "+strings.Join(fields[1:], " "))
		}
		out, err := cli.RunSlashCommandRPC(ctx, line)
		if err == nil && sub != "" && sub != "help" && cli.scheduler != nil && cli.schedulerRemote == nil {
			out += "\n\n" + i18n.T("web.command.schedule.in_process")
		}
		return out, err
	}
	return cli.RunSlashCommandRPC(ctx, line)
}

// PlanCommandRPC applies /plan for the web page exactly as the terminal
// handler does (it arms Plan-First for the next agent or coder run) and
// reports which mode should run which task: mode is "" when the command
// only armed the flag or printed its usage, in which case notice carries
// what the terminal would have printed.
func (cli *ChatCLI) PlanCommandRPC(ctx context.Context, line string) (mode, task, notice string) {
	var route planCommandRoute
	notice, _ = captureRPCStdout(ctx, func() error {
		route = cli.handlePlanCommand(strings.TrimSpace(line))
		return nil
	})
	if route == planRouteNone {
		return "", "", notice
	}
	rest := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "/plan"))
	head, tail := rest, ""
	if i := strings.IndexAny(rest, " \t"); i >= 0 {
		head, tail = rest[:i], strings.TrimSpace(rest[i:])
	}
	switch head {
	case "preview", "dry", "coder", "agent":
		task = tail
	default:
		task = rest
	}
	if route == planRouteCoder {
		return "coder", task, notice
	}
	return "agent", task, notice
}

// StageSkillRPC stages a user-invocable skill for the next turn the way
// typing /<skill> in the terminal does, and returns the prompt that turn
// sends. ok is false when name is no installed skill (or collides with a
// built-in command); a skill that exists but is not user-invocable
// returns ok with refused set to the notice to show instead.
func (cli *ChatCLI) StageSkillRPC(name, args string) (prompt, refused string, ok bool) {
	if cli.personaHandler == nil || cli.commandHandler == nil || name == "" || cli.commandHandler.isReservedSlashName(name) {
		return "", "", false
	}
	mgr := cli.personaHandler.GetManager()
	if mgr == nil {
		return "", "", false
	}
	skill, err := mgr.GetSkillByName(name)
	if err != nil || skill == nil {
		return "", "", false
	}
	if !skill.UserInvocable {
		return "", i18n.T("skill.invoke.not_invocable") + ": /" + name, true
	}
	cli.pendingManualSkill = skill
	cli.pendingManualSkillArgs = args
	prompt = args
	if prompt == "" {
		prompt = "Apply skill \"" + skill.Name + "\" to the current context."
	}
	return prompt, "", true
}
