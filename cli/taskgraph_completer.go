/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"strings"

	prompt "github.com/c-bata/go-prompt"
	"github.com/diillson/chatcli/cli/taskgraph"
	"github.com/diillson/chatcli/i18n"
)

// taskGraphSubcommandSuggestions returns /taskgraph subcommands (per call so
// i18n resolves with the active locale).
func taskGraphSubcommandSuggestions() []prompt.Suggest {
	return []prompt.Suggest{
		{Text: "status", Description: i18n.T("complete.taskgraph.status")},
		{Text: "show", Description: i18n.T("complete.taskgraph.show")},
		{Text: "list", Description: i18n.T("complete.taskgraph.list")},
		{Text: "dash", Description: i18n.T("complete.taskgraph.dash")},
		{Text: "prune", Description: i18n.T("complete.taskgraph.prune")},
		{Text: "cancel", Description: i18n.T("complete.taskgraph.cancel")},
		{Text: "help", Description: i18n.T("complete.taskgraph.help")},
	}
}

// getTaskGraphSuggestions completes /taskgraph subcommands and run IDs.
func (cli *ChatCLI) getTaskGraphSuggestions(d prompt.Document) []prompt.Suggest {
	line := strings.TrimPrefix(d.TextBeforeCursor(), "/taskgraph")
	line = strings.TrimLeft(line, " ")
	args := strings.Fields(line)
	current := d.GetWordBeforeCursor()
	trailingSpace := strings.HasSuffix(d.TextBeforeCursor(), " ")

	if len(args) == 0 || (len(args) == 1 && !trailingSpace) {
		return prompt.FilterHasPrefix(taskGraphSubcommandSuggestions(), current, true)
	}

	runIDSuggestions := func() []prompt.Suggest {
		base, err := cli.taskGraphBaseDir()
		if err != nil {
			return nil
		}
		rows, err := taskgraph.ListRuns(base)
		if err != nil {
			return nil
		}
		out := make([]prompt.Suggest, 0, len(rows))
		for _, r := range rows {
			out = append(out, prompt.Suggest{Text: r.RunID, Description: r.Name + " (" + string(r.Status) + ")"})
		}
		return out
	}

	switch args[0] {
	case "status", "st", "dash":
		return prompt.FilterHasPrefix(runIDSuggestions(), current, true)
	case "show", "task":
		// First arg is a task id (unknown here); the second is a run id.
		if len(args) >= 2 && trailingSpace || len(args) >= 3 {
			return prompt.FilterHasPrefix(runIDSuggestions(), current, true)
		}
	}
	return nil
}
