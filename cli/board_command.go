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

// boardUsageKey is the /board usage text: every verb with its arguments.
const boardUsageKey = "board.usage.verbs"

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

	// Every successful mutation sets this: the tool path taps the adapter's
	// onMutate, but a person's edit never crosses the adapter, so without
	// it the knowledge graph serves the old cards until its cache expires.
	mutated := false
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
	case "create", "add", "new":
		mutated = cli.boardCreate(userInput)
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
		mutated = true
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
		mutated = true
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
		mutated = true
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
		mutated = true
	case "unassign", "unclaim", "release":
		mutated = cli.boardUnassign(userInput)
	case "edit", "update", "rename":
		mutated = cli.boardEdit(userInput)
	case "link":
		mutated = cli.boardLink(userInput)
	case "delete", "remove", "rm":
		mutated = cli.boardDelete(userInput)
	case "help", "-h", "--help":
		cli.printBoardUsage()
	default:
		fmt.Println(colorize("  "+i18n.T("board.unknown", sub), ColorYellow))
		cli.printBoardUsage()
	}
	if mutated {
		cli.markGraphDirty()
	}
}

// boardReport prints the outcome of one board mutation and reports whether
// it changed the board.
func boardReport(err error, okMsg string) bool {
	if err != nil {
		fmt.Println(colorize("  ❌ "+err.Error(), ColorRed))
		return false
	}
	fmt.Println(colorize("  "+okMsg, ColorGreen))
	return true
}

// boardArgs parses a verb's tail, printing the problem and the usage when
// it does not parse.
func boardArgs(userInput string, spec map[string]squadFlagKind) (squadArgs, bool) {
	parsed, err := parseSquadTail(userInput, spec)
	if err != nil {
		printSquadNotice(err.Error())
		printSquadUsage(boardUsageKey)
		return parsed, false
	}
	return parsed, true
}

// boardCreate handles "create <title> [--description T] [--assignee A]
// [--column C]".
func (cli *ChatCLI) boardCreate(userInput string) bool {
	parsed, ok := boardArgs(userInput, map[string]squadFlagKind{
		"--description": squadFlagText, "--assignee": squadFlagWord, "--column": squadFlagWord,
	})
	if !ok {
		return false
	}
	title := parsed.positional(0)
	if title == "" {
		printSquadNotice(i18n.T("board.title.missing"))
		printSquadUsage(boardUsageKey)
		return false
	}
	col := board.ColBacklog
	if raw := parsed.values["--column"]; raw != "" {
		var err error
		if col, err = board.ParseColumn(raw); err != nil {
			printSquadNotice(i18n.T("board.column.invalid", raw))
			return false
		}
	}
	card, err := squadBoard().Create(title, parsed.values["--description"], parsed.values["--assignee"], col)
	if err != nil {
		return boardReport(err, "")
	}
	return boardReport(nil, i18n.T("board.create.done", card.ID, string(card.Column)))
}

// boardEdit handles "edit <id> [new title] [--title T] [--description T]".
func (cli *ChatCLI) boardEdit(userInput string) bool {
	parsed, ok := boardArgs(userInput, map[string]squadFlagKind{
		"--title": squadFlagText, "--description": squadFlagText,
	})
	if !ok {
		return false
	}
	title := parsed.values["--title"]
	if title != "" && len(parsed.pos) > 1 {
		printSquadNotice(i18n.T("squad.arg.twice", parsed.positional(1), "--title"))
		return false
	}
	if title == "" {
		title = parsed.positional(1)
	}
	description := parsed.values["--description"]
	if len(parsed.pos) == 0 || (title == "" && description == "") {
		printSquadUsage(boardUsageKey)
		return false
	}
	id := parsed.pos[0].text
	_, err := squadBoard().Update(id, title, description)
	return boardReport(err, i18n.T("board.edit.ok", id))
}

// boardUnassign handles "unassign <id>".
func (cli *ChatCLI) boardUnassign(userInput string) bool {
	parsed, ok := boardArgs(userInput, nil)
	if !ok {
		return false
	}
	if len(parsed.pos) != 1 {
		printSquadUsage(boardUsageKey)
		return false
	}
	id := parsed.pos[0].text
	_, err := squadBoard().Assign(id, "")
	return boardReport(err, i18n.T("board.unassign.ok", id))
}

// boardLink handles "link <id> [run-id] [--run R] [--job J]".
func (cli *ChatCLI) boardLink(userInput string) bool {
	parsed, ok := boardArgs(userInput, map[string]squadFlagKind{
		"--run": squadFlagWord, "--job": squadFlagWord,
	})
	if !ok {
		return false
	}
	runID, jobID := parsed.values["--run"], parsed.values["--job"]
	if runID != "" && len(parsed.pos) == 2 {
		printSquadNotice(i18n.T("squad.arg.twice", parsed.pos[1].raw, "--run"))
		return false
	}
	if len(parsed.pos) == 2 {
		runID = parsed.pos[1].text
	}
	if len(parsed.pos) == 0 || len(parsed.pos) > 2 || (runID == "" && jobID == "") {
		printSquadUsage(boardUsageKey)
		return false
	}
	id := parsed.pos[0].text
	store := squadBoard()
	// Checked up front so a bad ID links nothing instead of failing between
	// the run and the job.
	if _, err := store.Get(id); err != nil {
		printSquadNotice(i18n.T("board.show.notfound", id))
		return false
	}
	linked := false
	for _, link := range []struct {
		value string
		apply func(id, value string) (board.Card, error)
	}{{runID, store.LinkRun}, {jobID, store.LinkJob}} {
		if link.value == "" {
			continue
		}
		if _, err := link.apply(id, link.value); err != nil {
			fmt.Println(colorize("  ❌ "+err.Error(), ColorRed))
			return linked
		}
		linked = true
	}
	return boardReport(nil, i18n.T("board.link.ok", id))
}

// boardDelete handles "delete <id> [--yes]" in two steps: the first call
// shows the card and changes nothing, the second (--yes) removes it. No
// stdin confirmation: the prompt holds the terminal in raw mode while a
// command runs, and the headless surfaces have no stdin at all.
func (cli *ChatCLI) boardDelete(userInput string) bool {
	parsed, ok := boardArgs(userInput, map[string]squadFlagKind{"--yes": squadFlagBool})
	if !ok {
		return false
	}
	if len(parsed.pos) != 1 {
		printSquadUsage(boardUsageKey)
		return false
	}
	id := parsed.pos[0].text
	store := squadBoard()
	card, err := store.Get(id)
	if err != nil {
		printSquadNotice(i18n.T("board.show.notfound", id))
		return false
	}
	if !parsed.set["--yes"] {
		printSquadNotice(i18n.T("board.delete.preview"))
		cli.boardShow(id)
		printSquadNotice(i18n.T("board.delete.rerun", id))
		return false
	}
	_, err = store.Delete(id)
	return boardReport(err, i18n.T("board.delete.ok", id, truncateForUI(card.Title, 70)))
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
	printSquadUsage(boardUsageKey)
}
