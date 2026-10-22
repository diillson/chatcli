/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package memory

import (
	"testing"

	"go.uber.org/zap"
)

func TestManager_FactsMatchingAndForgetFactByID(t *testing.T) {
	m := NewManager(t.TempDir(), DefaultConfig(), zap.NewNop())
	if !m.RememberFact("alpha fact one", "project") || !m.RememberFact("beta fact two", "project") {
		t.Fatal("seed")
	}
	if got := m.FactsMatching("alpha"); len(got) != 1 || got[0].Content != "alpha fact one" {
		t.Fatalf("matching = %+v", got)
	}
	if f, n := m.ForgetFactByID(""); f != nil || n != 0 {
		t.Fatal("empty prefix removes nothing")
	}
	if f, n := m.ForgetFactByID("zzz"); f != nil || n != 0 {
		t.Fatalf("unknown prefix = %v %d", f, n)
	}
	all := m.Facts.GetAll()
	if len(all) != 2 {
		t.Fatalf("expected 2 facts, got %d", len(all))
	}
	id := all[0].ID
	f, n := m.ForgetFactByID(id)
	if f == nil || n != 1 || f.ID != id {
		t.Fatalf("forget by full id = %v %d", f, n)
	}
	if len(m.Facts.GetAll()) != 1 {
		t.Fatal("exactly one fact removed")
	}
}
