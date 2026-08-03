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
		{Text: "move", Description: i18n.T("complete.board.move")},
		{Text: "assign", Description: i18n.T("complete.board.assign")},
		{Text: "note", Description: i18n.T("complete.board.note")},
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

	switch args[0] {
	case "show", "move", "assign", "note":
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
