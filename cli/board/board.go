/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * Package board is the squad's shared work board: a small kanban of cards
 * (backlog → doing → review → blocked → done) that the orchestrator LLM
 * manages autonomously via the @board tool and humans inspect via /board.
 *
 * A card is the unit of work the squad collaborates on: it carries an
 * assignee (worker agent type), free-form notes (review verdicts, delivery
 * summaries), linked agent run IDs and scheduler job IDs, and a full
 * transition history. The board is what lets a single user prompt fan out
 * into develop → review → deliver without the user tracking anything.
 *
 * Persistence is a single JSON document written atomically (temp file +
 * rename) under the chatcli home dir. The store serializes all access with
 * a mutex; it is safe for concurrent use within one process. Cross-process
 * consumers (gateway daemon and REPL both open the board) get last-writer-
 * wins semantics per mutation — acceptable because every mutation rewrites
 * from the freshly loaded file (read-modify-write under lock).
 */
package board

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Column is a kanban column.
type Column string

const (
	ColBacklog Column = "backlog"
	ColDoing   Column = "doing"
	ColReview  Column = "review"
	ColBlocked Column = "blocked"
	ColDone    Column = "done"
)

// Columns is the canonical display order.
var Columns = []Column{ColBacklog, ColDoing, ColReview, ColBlocked, ColDone}

// ParseColumn maps a user/LLM-provided column name (with common aliases) to
// a canonical Column. Empty string is not valid.
func ParseColumn(s string) (Column, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "backlog", "todo", "pending":
		return ColBacklog, nil
	case "doing", "in_progress", "inprogress", "wip", "dev", "develop":
		return ColDoing, nil
	case "review", "reviewing", "qa":
		return ColReview, nil
	case "blocked", "waiting", "parked":
		return ColBlocked, nil
	case "done", "completed", "delivered", "closed":
		return ColDone, nil
	}
	return "", fmt.Errorf("invalid column %q (expected backlog|doing|review|blocked|done)", s)
}

// Note is a timestamped comment on a card (review verdict, delivery note…).
type Note struct {
	Author string    `json:"author"` // "orchestrator", "reviewer", "user", …
	Text   string    `json:"text"`
	At     time.Time `json:"at"`
}

// Transition records one column move.
type Transition struct {
	From Column    `json:"from"`
	To   Column    `json:"to"`
	By   string    `json:"by"`
	At   time.Time `json:"at"`
}

// Card is one unit of squad work.
type Card struct {
	ID          string       `json:"id"`
	Title       string       `json:"title"`
	Description string       `json:"description,omitempty"`
	Column      Column       `json:"column"`
	Assignee    string       `json:"assignee,omitempty"` // worker agent type
	Notes       []Note       `json:"notes,omitempty"`
	RunIDs      []string     `json:"run_ids,omitempty"` // linked agent runs
	JobIDs      []string     `json:"job_ids,omitempty"` // linked scheduler jobs
	History     []Transition `json:"history,omitempty"`
	CreatedAt   time.Time    `json:"created_at"`
	UpdatedAt   time.Time    `json:"updated_at"`
}

// boardDoc is the on-disk document.
type boardDoc struct {
	SchemaVersion int     `json:"schema_version"`
	Seq           uint64  `json:"seq"`
	Cards         []*Card `json:"cards"`
}

const schemaVersion = 1

// ErrNotFound is returned when a card ID does not exist.
var ErrNotFound = errors.New("card not found")

// Store is a mutex-serialized board with atomic file persistence.
type Store struct {
	mu   sync.Mutex
	path string
}

// DefaultPath resolves the board file location: CHATCLI_BOARD_PATH when set,
// otherwise ~/.chatcli/board.json.
func DefaultPath() string {
	if p := strings.TrimSpace(os.Getenv("CHATCLI_BOARD_PATH")); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", ".chatcli", "board.json")
	}
	return filepath.Join(home, ".chatcli", "board.json")
}

// NewStore opens a store at path ("" = DefaultPath()). The file is created
// lazily on first mutation.
func NewStore(path string) *Store {
	if strings.TrimSpace(path) == "" {
		path = DefaultPath()
	}
	return &Store{path: path}
}

var (
	defaultOnce  sync.Once
	defaultStore *Store
)

// Default returns the process-wide store over DefaultPath().
func Default() *Store {
	defaultOnce.Do(func() { defaultStore = NewStore("") })
	return defaultStore
}

// load reads the document from disk; a missing file yields an empty board.
// Corrupt files return an error rather than silently wiping the board.
func (s *Store) load() (*boardDoc, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return &boardDoc{SchemaVersion: schemaVersion}, nil
		}
		return nil, fmt.Errorf("board: read %s: %w", s.path, err)
	}
	var doc boardDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("board: corrupt file %s (fix or remove it): %w", s.path, err)
	}
	return &doc, nil
}

// save writes the document atomically: temp file in the same dir + rename.
func (s *Store) save(doc *boardDoc) error {
	doc.SchemaVersion = schemaVersion
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("board: mkdir %s: %w", dir, err)
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("board: marshal: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".board-*.tmp")
	if err != nil {
		return fmt.Errorf("board: temp file: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("board: write temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("board: close temp: %w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("board: rename: %w", err)
	}
	return nil
}

// mutate runs fn over the freshly loaded document and persists the result.
func (s *Store) mutate(fn func(doc *boardDoc) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	doc, err := s.load()
	if err != nil {
		return err
	}
	if err := fn(doc); err != nil {
		return err
	}
	return s.save(doc)
}

// findCard returns the card with id, or nil.
func findCard(doc *boardDoc, id string) *Card {
	for _, c := range doc.Cards {
		if c.ID == id {
			return c
		}
	}
	return nil
}

// Create adds a card to the backlog (or the given column) and returns it.
func (s *Store) Create(title, description, assignee string, col Column) (*Card, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return nil, errors.New("board: card title must not be empty")
	}
	if col == "" {
		col = ColBacklog
	}
	var created *Card
	err := s.mutate(func(doc *boardDoc) error {
		doc.Seq++
		now := time.Now()
		created = &Card{
			ID:          "card-" + strconv.FormatUint(doc.Seq, 10),
			Title:       title,
			Description: strings.TrimSpace(description),
			Column:      col,
			Assignee:    strings.TrimSpace(assignee),
			CreatedAt:   now,
			UpdatedAt:   now,
		}
		doc.Cards = append(doc.Cards, created)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return created, nil
}

// Get returns a copy of one card by ID.
func (s *Store) Get(id string) (Card, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	doc, err := s.load()
	if err != nil {
		return Card{}, err
	}
	c := findCard(doc, id)
	if c == nil {
		return Card{}, fmt.Errorf("board: %w: %s", ErrNotFound, id)
	}
	return *c, nil
}

// List returns copies of all cards, optionally filtered by column, ordered
// by column display order then creation time.
func (s *Store) List(col Column) ([]Card, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	doc, err := s.load()
	if err != nil {
		return nil, err
	}
	colOrder := make(map[Column]int, len(Columns))
	for i, c := range Columns {
		colOrder[c] = i
	}
	out := make([]Card, 0, len(doc.Cards))
	for _, c := range doc.Cards {
		if col != "" && c.Column != col {
			continue
		}
		out = append(out, *c)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if colOrder[out[i].Column] != colOrder[out[j].Column] {
			return colOrder[out[i].Column] < colOrder[out[j].Column]
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out, nil
}

// Move transitions a card to a new column, recording who moved it.
func (s *Store) Move(id string, to Column, by string) (Card, error) {
	var moved Card
	err := s.mutate(func(doc *boardDoc) error {
		c := findCard(doc, id)
		if c == nil {
			return fmt.Errorf("board: %w: %s", ErrNotFound, id)
		}
		if c.Column == to {
			moved = *c
			return nil
		}
		now := time.Now()
		c.History = append(c.History, Transition{From: c.Column, To: to, By: strings.TrimSpace(by), At: now})
		c.Column = to
		c.UpdatedAt = now
		moved = *c
		return nil
	})
	return moved, err
}

// Assign sets (or clears, with "") the card's assignee.
func (s *Store) Assign(id, assignee string) (Card, error) {
	var updated Card
	err := s.mutate(func(doc *boardDoc) error {
		c := findCard(doc, id)
		if c == nil {
			return fmt.Errorf("board: %w: %s", ErrNotFound, id)
		}
		c.Assignee = strings.TrimSpace(assignee)
		c.UpdatedAt = time.Now()
		updated = *c
		return nil
	})
	return updated, err
}

// AddNote appends a note to a card.
func (s *Store) AddNote(id, author, text string) (Card, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return Card{}, errors.New("board: note text must not be empty")
	}
	var updated Card
	err := s.mutate(func(doc *boardDoc) error {
		c := findCard(doc, id)
		if c == nil {
			return fmt.Errorf("board: %w: %s", ErrNotFound, id)
		}
		c.Notes = append(c.Notes, Note{Author: strings.TrimSpace(author), Text: text, At: time.Now()})
		c.UpdatedAt = time.Now()
		updated = *c
		return nil
	})
	return updated, err
}

// LinkRun associates an agent run ID with a card (deduplicated).
func (s *Store) LinkRun(id, runID string) (Card, error) {
	return s.appendLink(id, runID, func(c *Card) *[]string { return &c.RunIDs })
}

// LinkJob associates a scheduler job ID with a card (deduplicated).
func (s *Store) LinkJob(id, jobID string) (Card, error) {
	return s.appendLink(id, jobID, func(c *Card) *[]string { return &c.JobIDs })
}

func (s *Store) appendLink(id, value string, field func(*Card) *[]string) (Card, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return Card{}, errors.New("board: link value must not be empty")
	}
	var updated Card
	err := s.mutate(func(doc *boardDoc) error {
		c := findCard(doc, id)
		if c == nil {
			return fmt.Errorf("board: %w: %s", ErrNotFound, id)
		}
		list := field(c)
		for _, existing := range *list {
			if existing == value {
				updated = *c
				return nil
			}
		}
		*list = append(*list, value)
		c.UpdatedAt = time.Now()
		updated = *c
		return nil
	})
	return updated, err
}

// Archive removes cards in done older than the given age (0 = all done
// cards) and returns how many were removed.
func (s *Store) Archive(olderThan time.Duration) (int, error) {
	removed := 0
	err := s.mutate(func(doc *boardDoc) error {
		cutoff := time.Now().Add(-olderThan)
		kept := doc.Cards[:0]
		for _, c := range doc.Cards {
			if c.Column == ColDone && (olderThan == 0 || c.UpdatedAt.Before(cutoff)) {
				removed++
				continue
			}
			kept = append(kept, c)
		}
		doc.Cards = kept
		return nil
	})
	if err != nil {
		return 0, err
	}
	return removed, nil
}
