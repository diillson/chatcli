/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * /proc — the human's view over the background processes the agent started
 * with @proc, on the SAME supervisor the tool writes to. Observe and stop
 * only: starting stays with the model, behind the command validator.
 * Nothing here reads stdin or waits: stop only REQUESTS the stop (the
 * supervisor does TERM→KILL on its own goroutine), so a headless caller or
 * the mid-run stdin reader is never held. The supervisor belongs to the
 * chatcli PROCESS, but each session reads its own table of it: on a
 * multi-session server /proc lists and stops what THAT session's agent
 * started, and another session's id answers like one that does not exist.
 * One caller looks across owners: the person at this terminal's prompt,
 * with /proc list --all and /proc stop <id> --owner <owner> — the way out
 * when sessions hold the host's capacity. No headless or session-scoped
 * path can use them.
 */
package cli

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	prompt "github.com/c-bata/go-prompt"
	"github.com/diillson/chatcli/cli/agent/proc"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/pkg/textfold"
)

// procCommandArgs is one parsed /proc invocation.
type procCommandArgs struct {
	sub  string
	id   string
	grep *regexp.Regexp
	tail int
	// all (list) and owner (stop) look across process-table owners; only
	// the terminal's prompt may. byOwner tells "--owner terminal" (the
	// empty owner) from no --owner at all.
	all     bool
	owner   string
	byOwner bool
}

// procTerminalOwner is how the empty owner — the terminal session — is
// written on the command line and shown in /proc list --all.
const procTerminalOwner = "terminal"

// procCommandFlags are the flags each /proc subcommand reads.
var procCommandFlags = map[string]map[string]bool{
	"list": {"--all": true},
	"logs": {"--grep": true, "--tail": true},
	"stop": {"--owner": true},
}

// parseProcCommand reads "/proc <sub> [id] [--grep re] [--tail N]". What it
// does not understand is an error: a mistyped stop must never act.
func parseProcCommand(userInput string) (procCommandArgs, error) {
	fields := strings.Fields(userInput)[1:]
	out := procCommandArgs{sub: "list"}
	if len(fields) == 0 {
		return out, nil
	}
	switch sub := strings.ToLower(fields[0]); sub {
	case "list", "ls", "ps":
	case "logs", "log", "tail":
		out.sub = "logs"
	case "stop", "kill":
		out.sub = "stop"
	case "status", "st":
		out.sub = "status"
	default:
		return out, fmt.Errorf("%s", i18n.T("proc.cmd.unknown_sub", fields[0]))
	}
	for i := 1; i < len(fields); i++ {
		tok := fields[i]
		isFlag := strings.HasPrefix(tok, "--")
		switch {
		case isFlag && !procCommandFlags[out.sub][tok]:
			return out, fmt.Errorf("%s", i18n.T("execparity.unknown_flag", "/proc "+out.sub, tok))
		case tok == "--all":
			out.all = true
		case isFlag && (i+1 >= len(fields) || strings.HasPrefix(fields[i+1], "--")):
			return out, fmt.Errorf("%s", i18n.T("execparity.flag_needs_value", tok))
		case tok == "--owner":
			i++
			out.byOwner = true
			if out.owner = fields[i]; out.owner == procTerminalOwner {
				out.owner = ""
			}
		case tok == "--grep":
			i++
			re, err := regexp.Compile("(?i)" + fields[i])
			if err != nil {
				return out, fmt.Errorf("%s", i18n.T("proc.cmd.bad_grep", fields[i], err))
			}
			out.grep = re
		case tok == "--tail":
			i++
			n, err := strconv.Atoi(fields[i])
			if err != nil || n < 1 || n > proc.MaxTailLines {
				return out, fmt.Errorf("%s", i18n.T("proc.cmd.bad_tail", fields[i], proc.MaxTailLines))
			}
			out.tail = n
		case out.id == "" && out.sub != "list":
			out.id = tok
		default:
			return out, fmt.Errorf("%s", i18n.T("execparity.extra_arg", "/proc "+out.sub, tok))
		}
	}
	if out.id == "" && out.sub != "list" {
		return out, fmt.Errorf("%s", i18n.T("proc.cmd.missing_id", out.sub))
	}
	return out, nil
}

// handleProcCommand routes /proc subcommands over the process table of the
// session ctx is marked with (the terminal's when it carries none).
func (cli *ChatCLI) handleProcCommand(ctx context.Context, userInput string) {
	args, err := parseProcCommand(userInput)
	if err != nil {
		fmt.Println(colorize("  "+err.Error(), ColorYellow))
		fmt.Println(colorize("  "+i18n.T("proc.cmd.usage"), ColorGray))
		if isLocalOperator(ctx) {
			fmt.Println(colorize("  "+i18n.T("leftovers.proc.usage_operator"), ColorGray))
		}
		return
	}
	adapter := &procToolAdapter{cli: cli, owner: proc.OwnerFrom(ctx)}
	if args.all || args.byOwner {
		// Looking across owners is the local operator's alone: a session,
		// a headless caller, a scheduled job or a command issued while a
		// turn runs gets the refusal and nothing else.
		if !isLocalOperator(ctx) {
			fmt.Println(colorize("  "+i18n.T("leftovers.proc.operator_only"), ColorYellow))
			return
		}
		if args.all {
			printProcAllOwners(adapter.processSupervisor().List())
			return
		}
		adapter.owner = args.owner
	}
	sup := adapter.supervisor()
	if args.sub == "list" {
		infos := sup.List()
		if len(infos) == 0 {
			fmt.Println(colorize("  "+i18n.T("proc.cmd.none_tracked"), ColorGray))
		}
		for _, info := range infos {
			fmt.Println("  - " + procHumanLine(info))
		}
		return
	}
	out, err := procCommandResult(sup, args)
	if err != nil {
		// The supervisor only fails on an id it does not track, and words
		// that for the model ("use @proc start"): answer in /proc terms.
		fmt.Println(colorize("  "+i18n.T("proc.cmd.unknown_id", args.id), ColorYellow))
		return
	}
	for _, line := range strings.Split(out, "\n") {
		fmt.Println("  " + line)
	}
}

// printProcAllOwners is /proc list --all: every tracked process, each
// under the owner whose table holds it.
func printProcAllOwners(infos []proc.Info) {
	if len(infos) == 0 {
		fmt.Println(colorize("  "+i18n.T("proc.cmd.none_tracked"), ColorGray))
		return
	}
	sort.SliceStable(infos, func(i, j int) bool { return infos[i].Owner < infos[j].Owner })
	for _, info := range infos {
		fmt.Printf("  - [%s] %s\n", procOwnerName(info.Owner), procHumanLine(info))
	}
}

// procOwnerName is an owner as the operator reads and types it.
func procOwnerName(owner string) string {
	if owner == "" {
		return procTerminalOwner
	}
	return owner
}

// procCommandResult renders status, logs or stop of one process.
func procCommandResult(sup *proc.Table, args procCommandArgs) (string, error) {
	switch args.sub {
	case "stop":
		info, err := sup.StopAsync(args.id)
		if err != nil || info.State != proc.StateRunning {
			return procHumanLine(info), err
		}
		return i18n.T("proc.cmd.stopping", info.ID, info.ID), err
	case "logs":
		if args.grep != nil {
			lines, matched, total, info, err := sup.LogsMatching(args.id, args.grep, args.tail)
			head := procHumanLine(info) + "\n" + i18n.T("proc.cmd.grep_header", len(lines), matched, total)
			return strings.TrimRight(head+"\n"+strings.Join(lines, "\n"), "\n"), err
		}
		logs, info, err := sup.Logs(args.id, args.tail)
		if strings.TrimSpace(logs) == "" {
			return procHumanLine(info) + "\n" + i18n.T("proc.cmd.no_output"), err
		}
		return procHumanLine(info) + "\n---\n" + strings.TrimRight(logs, "\n"), err
	default:
		info, err := sup.Status(args.id)
		return procHumanLine(info), err
	}
}

// procHumanLine is the localized one-line snapshot of a process.
func procHumanLine(info proc.Info) string {
	cmd := info.Command
	if len(cmd) > 80 {
		cmd = textfold.Cut(cmd, 77) + "..."
	}
	if info.State == proc.StateRunning {
		return i18n.T("proc.cmd.row_running", info.ID, strconv.Itoa(info.PID), time.Since(info.Started).Round(time.Second), cmd)
	}
	line := i18n.T("proc.cmd.row_exited", info.ID, info.ExitCode, info.Ended.Sub(info.Started).Round(time.Second), cmd)
	if info.PipesAbandoned {
		line += " — " + i18n.T("proc.cmd.pipes_abandoned")
	}
	return line
}

// getProcSuggestions completes subcommands, live process ids, logs flags.
func (cli *ChatCLI) getProcSuggestions(d prompt.Document) []prompt.Suggest {
	args := strings.Fields(strings.TrimPrefix(d.TextBeforeCursor(), "/proc"))
	current := d.GetWordBeforeCursor()
	trailingSpace := strings.HasSuffix(d.TextBeforeCursor(), " ")

	if len(args) == 0 || (len(args) == 1 && !trailingSpace) {
		return prompt.FilterHasPrefix([]prompt.Suggest{
			{Text: "list", Description: i18n.T("complete.proc.list")},
			{Text: "status", Description: i18n.T("complete.proc.status")},
			{Text: "logs", Description: i18n.T("complete.proc.logs")},
			{Text: "stop", Description: i18n.T("complete.proc.stop")},
		}, current, true)
	}
	switch args[0] {
	case "logs", "stop", "status":
	case "list":
		// The completer only runs at the terminal's prompt, which is where
		// the operator flags work.
		return prompt.FilterHasPrefix([]prompt.Suggest{
			{Text: "--all", Description: i18n.T("complete.leftovers.proc.all")},
		}, current, true)
	default:
		return nil
	}
	if len(args) == 1 || (len(args) == 2 && !trailingSpace) {
		var out []prompt.Suggest
		for _, info := range (&procToolAdapter{cli: cli}).supervisor().List() {
			out = append(out, prompt.Suggest{Text: info.ID, Description: procHumanLine(info)})
		}
		return prompt.FilterHasPrefix(out, current, true)
	}
	if args[0] == "stop" {
		return cli.getProcOwnerSuggestions(args, current, trailingSpace)
	}
	if args[0] != "logs" {
		return nil
	}
	return prompt.FilterHasPrefix([]prompt.Suggest{
		{Text: "--grep", Description: i18n.T("complete.proc.grep")},
		{Text: "--tail", Description: i18n.T("complete.proc.tail")},
	}, current, true)
}

// getProcOwnerSuggestions completes "/proc stop <id> --owner <owner>": the
// flag after the id, then the owners that have a process.
func (cli *ChatCLI) getProcOwnerSuggestions(args []string, current string, trailingSpace bool) []prompt.Suggest {
	last := args[len(args)-1]
	if (last == "--owner" && trailingSpace) || (len(args) >= 2 && args[len(args)-2] == "--owner" && !trailingSpace) {
		seen := map[string]bool{}
		var out []prompt.Suggest
		for _, info := range (&procToolAdapter{cli: cli}).processSupervisor().List() {
			if name := procOwnerName(info.Owner); !seen[name] {
				seen[name] = true
				out = append(out, prompt.Suggest{Text: name})
			}
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Text < out[j].Text })
		return prompt.FilterHasPrefix(out, current, true)
	}
	for _, a := range args {
		if a == "--owner" {
			return nil
		}
	}
	return prompt.FilterHasPrefix([]prompt.Suggest{
		{Text: "--owner", Description: i18n.T("complete.leftovers.proc.owner")},
	}, current, true)
}
