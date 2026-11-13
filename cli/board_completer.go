/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"strings"

	prompt "github.com/c-bata/go-prompt"
	"github.com/diillson/chatcli/cli/board"
	"github.com/diillson/chatcli/i18n"
)

// boardSubcommandSuggestions returns /board subcommands (per call so i18n
// resolves with the active locale).
func boardSubcommandSuggestions() []prompt.Suggest {
	return []prompt.Suggest{
		{Text: "list", Description: i18n.T("complete.board.list")},
		{Text: "show", Description: i18n.T("complete.board.show")},
		{Text: "create", Description: i18n.T("complete.board.create")},
		{Text: "edit", Description: i18n.T("complete.board.edit")},
		{Text: "move", Description: i18n.T("complete.board.move")},
		{Text: "assign", Description: i18n.T("complete.board.assign")},
		{Text: "unassign", Description: i18n.T("complete.board.unassign")},
		{Text: "note", Description: i18n.T("complete.board.note")},
		{Text: "link", Description: i18n.T("complete.board.link")},
		{Text: "delete", Description: i18n.T("complete.board.delete")},
		{Text: "archive", Description: i18n.T("complete.board.archive")},
		{Text: "help", Description: i18n.T("complete.board.help")},
	}
}

// boardColumnSuggestions lists the kanban columns.
func boardColumnSuggestions() []prompt.Suggest {
	out := make([]prompt.Suggest, 0, len(board.Columns))
	for _, col := range board.Columns {
		out = append(out, prompt.Suggest{Text: string(col)})
	}
	return out
}

// getBoardSuggestions completes /board subcommands, card IDs and columns.
func (cli *ChatCLI) getBoardSuggestions(d prompt.Document) []prompt.Suggest {
	line := strings.TrimPrefix(d.TextBeforeCursor(), "/board")
	line = strings.TrimLeft(line, " ")
	args := strings.Fields(line)
	current := d.GetWordBeforeCursor()
	trailingSpace := strings.HasSuffix(d.TextBeforeCursor(), " ")

	if len(args) == 0 || (len(args) == 1 && !trailingSpace) {
		return prompt.FilterHasPrefix(boardSubcommandSuggestions(), current, true)
	}

	cardIDSuggestions := func() []prompt.Suggest {
		cards, err := board.Default().List("")
		if err != nil {
			return nil
		}
		var out []prompt.Suggest
		for _, c := range cards {
			out = append(out, prompt.Suggest{
				Text:        c.ID,
				Description: string(c.Column) + " — " + truncateForUI(c.Title, 40),
			})
		}
		return out
	}

	if len(args) >= 2 {
		if flags := boardFlagSuggestions(args, current, trailingSpace); len(flags) > 0 {
			return flags
		}
	}

	switch args[0] {
	case "show", "info", "move", "assign", "note", "edit", "unassign", "link", "delete":
		if len(args) == 1 || (len(args) == 2 && !trailingSpace) {
			return prompt.FilterHasPrefix(cardIDSuggestions(), current, true)
		}
		if args[0] == "move" && (len(args) == 2 || (len(args) == 3 && !trailingSpace)) {
			return prompt.FilterHasPrefix(boardColumnSuggestions(), current, true)
		}
	case "list":
		if len(args) == 1 || (len(args) == 2 && !trailingSpace) {
			return prompt.FilterHasPrefix(boardColumnSuggestions(), current, true)
		}
	}
	return nil
}

// boardFlagSuggestions completes the flags of the verbs that take them,
// and the column after "--column". Nil means the verb has none to offer.
func boardFlagSuggestions(args []string, current string, trailingSpace bool) []prompt.Suggest {
	previous := args[len(args)-1]
	if !trailingSpace && len(args) >= 2 {
		previous = args[len(args)-2]
	}
	if previous == "--column" {
		return prompt.FilterHasPrefix(boardColumnSuggestions(), current, true)
	}
	if !trailingSpace {
		args = args[:len(args)-1] // the word being typed is not "used" yet
	}
	switch args[0] {
	case "create", "add", "new":
		if !strings.HasPrefix(current, "-") {
			return nil // still typing the title
		}
		return squadFlagSuggestions(args, current,
			prompt.Suggest{Text: "--description", Description: i18n.T("complete.board.flag.description")},
			prompt.Suggest{Text: "--assignee", Description: i18n.T("complete.board.flag.assignee")},
			prompt.Suggest{Text: "--column", Description: i18n.T("complete.board.flag.column")})
	case "edit", "update", "rename":
		return squadFlagSuggestions(args, current,
			prompt.Suggest{Text: "--title", Description: i18n.T("complete.board.flag.title")},
			prompt.Suggest{Text: "--description", Description: i18n.T("complete.board.flag.description")})
	case "link":
		return squadFlagSuggestions(args, current,
			prompt.Suggest{Text: "--run", Description: i18n.T("complete.board.flag.run")},
			prompt.Suggest{Text: "--job", Description: i18n.T("complete.board.flag.job")})
	case "delete", "remove", "rm":
		return squadFlagSuggestions(args, current,
			prompt.Suggest{Text: "--yes", Description: i18n.T("complete.board.flag.yes")})
	}
	return nil
}
