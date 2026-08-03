/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * /board — human view over the squad work board.
 *
 * Renders the same kanban the orchestrator LLM manages via @board:
 * cards grouped by column with assignee, linked runs and note counts.
 */
package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/diillson/chatcli/cli/board"
	"github.com/diillson/chatcli/i18n"
)

// squadBoard resolves the board store; a package-level indirection so
// tests can point the command layer at a temp store.
var squadBoard = board.Default

// handleBoardCommand routes /board subcommands.
func (cli *ChatCLI) handleBoardCommand(userInput string) {
	args := strings.Fields(strings.TrimSpace(userInput))
	sub := "list"
	if len(args) >= 2 {
		sub = args[1]
	}
	var rest []string
	if len(args) > 2 {
		rest = args[2:]
	}

	switch sub {
	case "list", "ls":
		var filter board.Column
		if len(rest) > 0 {
			parsed, err := board.ParseColumn(rest[0])
			if err != nil {
				fmt.Println(colorize("  "+i18n.T("board.column.invalid", rest[0]), ColorYellow))
				return
			}
			filter = parsed
		}
		cli.boardList(filter)
	case "show", "info":
		if len(rest) < 1 {
			fmt.Println(colorize("  "+i18n.T("board.id.missing"), ColorYellow))
			cli.printBoardUsage()
			return
		}
		cli.boardShow(rest[0])
	case "create", "add":
		title := strings.TrimSpace(strings.Join(rest, " "))
		if title == "" {
			fmt.Println(colorize("  "+i18n.T("board.title.missing"), ColorYellow))
			cli.printBoardUsage()
			return
		}
		card, err := squadBoard().Create(title, "", "", "")
		if err != nil {
			fmt.Println(colorize("  ❌ "+err.Error(), ColorRed))
			return
		}
		fmt.Println(colorize("  "+i18n.T("board.create.ok", card.ID), ColorGreen))
	case "move":
		if len(rest) < 2 {
			fmt.Println(colorize("  "+i18n.T("board.move.usage"), ColorYellow))
			return
		}
		col, err := board.ParseColumn(rest[1])
		if err != nil {
			fmt.Println(colorize("  "+i18n.T("board.column.invalid", rest[1]), ColorYellow))
			return
		}
		if _, err := squadBoard().Move(rest[0], col, "user"); err != nil {
			fmt.Println(colorize("  ❌ "+err.Error(), ColorRed))
			return
		}
		fmt.Println(colorize("  "+i18n.T("board.move.ok", rest[0], string(col)), ColorGreen))
	case "assign":
		if len(rest) < 2 {
			fmt.Println(colorize("  "+i18n.T("board.assign.usage"), ColorYellow))
			return
		}
		if _, err := squadBoard().Assign(rest[0], rest[1]); err != nil {
			fmt.Println(colorize("  ❌ "+err.Error(), ColorRed))
			return
		}
		fmt.Println(colorize("  "+i18n.T("board.assign.ok", rest[0], rest[1]), ColorGreen))
	case "note", "comment":
		if len(rest) < 2 {
			fmt.Println(colorize("  "+i18n.T("board.note.usage"), ColorYellow))
			return
		}
		text := strings.TrimSpace(strings.Join(rest[1:], " "))
		if _, err := squadBoard().AddNote(rest[0], "user", text); err != nil {
			fmt.Println(colorize("  ❌ "+err.Error(), ColorRed))
			return
		}
		fmt.Println(colorize("  "+i18n.T("board.note.ok", rest[0]), ColorGreen))
	case "archive", "gc":
		var age time.Duration
		if len(rest) > 0 {
			parsed, err := time.ParseDuration(rest[0])
			if err != nil {
				fmt.Println(colorize("  "+i18n.T("board.archive.invalid", rest[0]), ColorYellow))
				return
			}
			age = parsed
		}
		n, err := squadBoard().Archive(age)
		if err != nil {
			fmt.Println(colorize("  ❌ "+err.Error(), ColorRed))
			return
		}
		fmt.Println(colorize("  "+i18n.T("board.archive.ok", n), ColorGreen))
	case "help", "-h", "--help":
		cli.printBoardUsage()
	default:
		fmt.Println(colorize("  "+i18n.T("board.unknown", sub), ColorYellow))
		cli.printBoardUsage()
	}
}

// boardList renders the kanban grouped by column.
func (cli *ChatCLI) boardList(filter board.Column) {
	cards, err := squadBoard().List(filter)
	if err != nil {
		fmt.Println(colorize("  ❌ "+err.Error(), ColorRed))
		return
	}
	if len(cards) == 0 {
		fmt.Println(colorize("  "+i18n.T("board.list.empty"), ColorGray))
		return
	}
	current := board.Column("")
	for _, c := range cards {
		if c.Column != current {
			current = c.Column
			fmt.Println(colorize(fmt.Sprintf("  %s %s", boardColumnIcon(current), strings.ToUpper(string(current))), ColorCyan+ColorBold))
		}
		fmt.Println(formatBoardCardHuman(c))
	}
}

// boardShow renders one card in full.
func (cli *ChatCLI) boardShow(id string) {
	card, err := squadBoard().Get(id)
	if err != nil {
		fmt.Println(colorize("  "+i18n.T("board.show.notfound", id), ColorYellow))
		return
	}
	fmt.Println(formatBoardCardHuman(card))
	kvLine := func(label, value string) {
		fmt.Printf("      %s %s\n", colorize(label+":", ColorGray), value)
	}
	if card.Description != "" {
		kvLine(i18n.T("board.field.description"), card.Description)
	}
	if len(card.RunIDs) > 0 {
		kvLine(i18n.T("board.field.runs"), strings.Join(card.RunIDs, ", "))
	}
	if len(card.JobIDs) > 0 {
		kvLine(i18n.T("board.field.jobs"), strings.Join(card.JobIDs, ", "))
	}
	kvLine(i18n.T("board.field.created"), card.CreatedAt.Format("2006-01-02 15:04"))
	if len(card.History) > 0 {
		fmt.Println(colorize("      "+i18n.T("board.field.history")+":", ColorGray))
		for _, h := range card.History {
			fmt.Printf("        %s → %s (%s, %s)\n", h.From, h.To, h.By, h.At.Format("15:04:05"))
		}
	}
	if len(card.Notes) > 0 {
		fmt.Println(colorize("      "+i18n.T("board.field.notes")+":", ColorGray))
		for _, n := range card.Notes {
			fmt.Printf("        [%s %s] %s\n", n.Author, n.At.Format("15:04"), n.Text)
		}
	}
}

// formatBoardCardHuman renders one card as a single terminal line.
func formatBoardCardHuman(c board.Card) string {
	var parts []string
	parts = append(parts, c.ID)
	if c.Assignee != "" {
		parts = append(parts, "@"+c.Assignee)
	}
	if len(c.RunIDs) > 0 {
		parts = append(parts, fmt.Sprintf("%d runs", len(c.RunIDs)))
	}
	if len(c.Notes) > 0 {
		parts = append(parts, fmt.Sprintf("%d notas", len(c.Notes)))
	}
	return fmt.Sprintf("    %s %s %s",
		colorize("▪", ColorPurple),
		colorize(strings.Join(parts, " · "), ColorBold),
		truncateForUI(c.Title, 70))
}

// boardColumnIcon returns the display glyph for a column header.
func boardColumnIcon(col board.Column) string {
	switch col {
	case board.ColBacklog:
		return "▤"
	case board.ColDoing:
		return "▶"
	case board.ColReview:
		return "◔"
	case board.ColBlocked:
		return "⛔"
	case board.ColDone:
		return "✓"
	}
	return "▪"
}

// printBoardUsage prints /board usage help.
func (cli *ChatCLI) printBoardUsage() {
	fmt.Println(colorize("  "+i18n.T("board.usage"), ColorGray))
}
