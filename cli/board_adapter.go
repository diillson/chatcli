/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/diillson/chatcli/cli/board"
)

// liveBoardAdapter implements plugins.BoardAdapter over the squad board
// store. Output is compact English text consumed by the LLM; the /board
// command renders its own i18n view for humans.
type liveBoardAdapter struct {
	store *board.Store
	// onMutate fires after every successful board mutation. Wired to
	// markGraphDirty so card nodes in the knowledge graph refresh promptly —
	// the tool path never crosses the command handler, so without its own
	// tap the graph would serve stale board state until the fingerprint TTL.
	onMutate func()
}

// newLiveBoardAdapter builds the adapter (nil store = board.Default()).
func newLiveBoardAdapter(store *board.Store) *liveBoardAdapter {
	if store == nil {
		store = board.Default()
	}
	return &liveBoardAdapter{store: store}
}

// mutated invokes the mutation tap, if any.
func (a *liveBoardAdapter) mutated() {
	if a.onMutate != nil {
		a.onMutate()
	}
}

// Create implements plugins.BoardAdapter.
func (a *liveBoardAdapter) Create(title, description, assignee, column string) (string, error) {
	col := board.ColBacklog
	if strings.TrimSpace(column) != "" {
		parsed, err := board.ParseColumn(column)
		if err != nil {
			return "", fmt.Errorf("@board create: %w", err)
		}
		col = parsed
	}
	card, err := a.store.Create(title, description, assignee, col)
	if err != nil {
		return "", err
	}
	a.mutated()
	return "created " + formatCardLine(*card), nil
}

// List implements plugins.BoardAdapter.
func (a *liveBoardAdapter) List(column string) (string, error) {
	var filter board.Column
	if strings.TrimSpace(column) != "" {
		parsed, err := board.ParseColumn(column)
		if err != nil {
			return "", fmt.Errorf("@board list: %w", err)
		}
		filter = parsed
	}
	cards, err := a.store.List(filter)
	if err != nil {
		return "", err
	}
	if len(cards) == 0 {
		return "BOARD EMPTY (no cards match)", nil
	}
	var b strings.Builder
	current := board.Column("")
	for _, c := range cards {
		if c.Column != current {
			current = c.Column
			fmt.Fprintf(&b, "== %s ==\n", strings.ToUpper(string(current)))
		}
		b.WriteString(formatCardLine(c))
		b.WriteString("\n")
	}
	return b.String(), nil
}

// Show implements plugins.BoardAdapter.
func (a *liveBoardAdapter) Show(id string) (string, error) {
	card, err := a.store.Get(id)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "id=%s\ntitle=%s\ncolumn=%s\n", card.ID, card.Title, card.Column)
	if card.Assignee != "" {
		fmt.Fprintf(&b, "assignee=%s\n", card.Assignee)
	}
	if card.Description != "" {
		fmt.Fprintf(&b, "description=%s\n", card.Description)
	}
	if len(card.RunIDs) > 0 {
		fmt.Fprintf(&b, "runs=%s\n", strings.Join(card.RunIDs, ","))
	}
	if len(card.JobIDs) > 0 {
		fmt.Fprintf(&b, "jobs=%s\n", strings.Join(card.JobIDs, ","))
	}
	fmt.Fprintf(&b, "created=%s\nupdated=%s\n",
		card.CreatedAt.Format(time.RFC3339), card.UpdatedAt.Format(time.RFC3339))
	if len(card.History) > 0 {
		b.WriteString("history:\n")
		for _, h := range card.History {
			fmt.Fprintf(&b, "  %s -> %s by=%s at=%s\n", h.From, h.To, h.By, h.At.Format("15:04:05"))
		}
	}
	if len(card.Notes) > 0 {
		b.WriteString("notes:\n")
		for _, n := range card.Notes {
			fmt.Fprintf(&b, "  [%s %s] %s\n", n.Author, n.At.Format("15:04:05"), n.Text)
		}
	}
	return b.String(), nil
}

// Move implements plugins.BoardAdapter.
func (a *liveBoardAdapter) Move(id, to, by string) (string, error) {
	col, err := board.ParseColumn(to)
	if err != nil {
		return "", fmt.Errorf("@board move: %w", err)
	}
	if strings.TrimSpace(by) == "" {
		by = "orchestrator"
	}
	card, err := a.store.Move(id, col, by)
	if err != nil {
		return "", err
	}
	a.mutated()
	return "moved " + formatCardLine(card), nil
}

// Assign implements plugins.BoardAdapter.
func (a *liveBoardAdapter) Assign(id, assignee string) (string, error) {
	card, err := a.store.Assign(id, assignee)
	if err != nil {
		return "", err
	}
	a.mutated()
	return "assigned " + formatCardLine(card), nil
}

// Note implements plugins.BoardAdapter.
func (a *liveBoardAdapter) Note(id, author, text string) (string, error) {
	if strings.TrimSpace(author) == "" {
		author = "orchestrator"
	}
	card, err := a.store.AddNote(id, author, text)
	if err != nil {
		return "", err
	}
	a.mutated()
	return fmt.Sprintf("note added to %s (%d notes total)", card.ID, len(card.Notes)), nil
}

// Link implements plugins.BoardAdapter.
func (a *liveBoardAdapter) Link(id, runID, jobID string) (string, error) {
	var card board.Card
	var err error
	if strings.TrimSpace(runID) != "" {
		if card, err = a.store.LinkRun(id, runID); err != nil {
			return "", err
		}
	}
	if strings.TrimSpace(jobID) != "" {
		if card, err = a.store.LinkJob(id, jobID); err != nil {
			return "", err
		}
	}
	a.mutated()
	return fmt.Sprintf("linked %s (runs=%s jobs=%s)", card.ID,
		strings.Join(card.RunIDs, ","), strings.Join(card.JobIDs, ",")), nil
}

// Archive implements plugins.BoardAdapter.
func (a *liveBoardAdapter) Archive(olderThan string) (string, error) {
	var age time.Duration
	if strings.TrimSpace(olderThan) != "" {
		parsed, err := time.ParseDuration(olderThan)
		if err != nil {
			return "", fmt.Errorf("@board archive: invalid older_than %q: %w", olderThan, err)
		}
		age = parsed
	}
	n, err := a.store.Archive(age)
	if err != nil {
		return "", err
	}
	a.mutated()
	return fmt.Sprintf("archived %d done card(s)", n), nil
}

// formatCardLine renders one card as a single compact line for the LLM.
func formatCardLine(c board.Card) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s [%s]", c.ID, c.Column)
	if c.Assignee != "" {
		fmt.Fprintf(&b, " assignee=%s", c.Assignee)
	}
	if len(c.RunIDs) > 0 {
		fmt.Fprintf(&b, " runs=%s", strings.Join(c.RunIDs, ","))
	}
	if len(c.Notes) > 0 {
		fmt.Fprintf(&b, " notes=%d", len(c.Notes))
	}
	fmt.Fprintf(&b, " title=%q", truncateForUI(c.Title, 80))
	return b.String()
}
