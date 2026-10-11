/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

func TestPrincipalSessionFactory_KeepsEachPrincipalInItsOwnDirectory(t *testing.T) {
	root := t.TempDir()
	open := principalSessionFactory(root, zap.NewNop())

	keyA := strings.Repeat("a", 64)
	keyB := strings.Repeat("b", 64)
	storeA, err := open(keyA)
	if err != nil {
		t.Fatalf("open A: %v", err)
	}
	storeB, err := open(keyB)
	if err != nil {
		t.Fatalf("open B: %v", err)
	}
	if err := storeA.SaveSession("notes", []models.Message{{Role: "user", Content: "a"}}); err != nil {
		t.Fatalf("save A: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, keyA, "sessions", "notes.json")); err != nil {
		t.Fatalf("A's session is not under its own directory: %v", err)
	}
	names, err := storeB.ListSessions()
	if err != nil {
		t.Fatalf("list B: %v", err)
	}
	if len(names) != 0 {
		t.Fatalf("B sees %v", names)
	}
}

func TestPrincipalSessionFactory_RefusesNonDigestKeys(t *testing.T) {
	root := t.TempDir()
	open := principalSessionFactory(root, zap.NewNop())
	for _, key := range []string{"", "..", "../../etc", strings.Repeat("A", 64), strings.Repeat("a", 63), strings.Repeat("a", 63) + "/"} {
		if _, err := open(key); err == nil {
			t.Errorf("key %q accepted", key)
		}
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read root: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("refused keys created directories: %v", entries)
	}
}
