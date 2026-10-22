/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package board

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestStore_UpdateAndDelete(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "board.json"))
	created, err := store.Create("first", "desc", "coder", ColDoing)
	if err != nil {
		t.Fatal(err)
	}

	// Nothing to change is refused and nothing is persisted.
	if _, err := store.Update(created.ID, "", "  "); !errors.Is(err, ErrNothingToChange) {
		t.Fatalf("Update with no fields: err=%v, want ErrNothingToChange", err)
	}

	// Title only keeps the description.
	card, err := store.Update(created.ID, "renamed", "")
	if err != nil {
		t.Fatal(err)
	}
	if card.Title != "renamed" || card.Description != "desc" || card.Assignee != "coder" {
		t.Fatalf("after title update: %+v", card)
	}
	if !card.UpdatedAt.After(created.UpdatedAt) && !card.UpdatedAt.Equal(created.UpdatedAt) {
		t.Fatalf("UpdatedAt not bumped: %v vs %v", card.UpdatedAt, created.UpdatedAt)
	}

	// Description only keeps the title; the change survives a fresh load.
	if _, err := store.Update(created.ID, "", "new desc"); err != nil {
		t.Fatal(err)
	}
	reloaded := NewStore(store.path)
	got, err := reloaded.Get(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "renamed" || got.Description != "new desc" {
		t.Fatalf("persisted card = %+v", got)
	}

	if _, err := store.Update("card-999", "x", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Update unknown: err=%v, want ErrNotFound", err)
	}

	// Delete returns the card as it was and removes it durably.
	removed, err := store.Delete(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if removed.ID != created.ID || removed.Title != "renamed" || removed.Column != ColDoing {
		t.Fatalf("Delete echoed %+v", removed)
	}
	if _, err := NewStore(store.path).Get(created.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("card still present after Delete: err=%v", err)
	}
	if _, err := store.Delete(created.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second Delete: err=%v, want ErrNotFound", err)
	}
}
