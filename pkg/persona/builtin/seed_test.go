/*
 * ChatCLI - Persona System
 * pkg/persona/builtin/seed_test.go
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package builtin

import (
	"os"
	"path/filepath"
	"testing"
)

// The essential set must always be embedded — guards against an accidental
// move/rename that would silently ship an empty bundle.
func TestEmbeddedSkillsPresent(t *testing.T) {
	skills, err := embeddedSkills()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"send-message", "email", "calendar", "reminders"} {
		if _, ok := skills[want]; !ok {
			t.Errorf("essential skill %q not embedded (got %v)", want, keys(skills))
		}
	}
}

func TestSeed_InstallsThenIdempotent(t *testing.T) {
	dir := t.TempDir()

	res, err := Seed(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Installed) == 0 {
		t.Fatal("expected first Seed to install skills")
	}
	// Files actually exist.
	if _, err := os.Stat(filepath.Join(dir, "send-message", "SKILL.md")); err != nil {
		t.Fatalf("send-message not written: %v", err)
	}
	// Manifest written.
	if _, err := os.Stat(filepath.Join(dir, manifestName)); err != nil {
		t.Fatalf("manifest not written: %v", err)
	}

	// Second run: nothing installed/updated, all unchanged.
	res2, err := Seed(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res2.Installed) != 0 || len(res2.Updated) != 0 {
		t.Fatalf("second Seed should be a no-op, got installed=%v updated=%v", res2.Installed, res2.Updated)
	}
	if len(res2.Unchanged) == 0 {
		t.Fatal("second Seed should report unchanged skills")
	}
}

func TestSeed_PreservesUserEdits(t *testing.T) {
	dir := t.TempDir()
	if _, err := Seed(dir, nil); err != nil {
		t.Fatal(err)
	}

	// User edits a seeded skill.
	target := filepath.Join(dir, "email", "SKILL.md")
	if err := os.WriteFile(target, []byte("my own email skill\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	res, err := Seed(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(res.Preserved, "email") {
		t.Fatalf("edited skill should be preserved, got %+v", res)
	}
	got, _ := os.ReadFile(target)
	if string(got) != "my own email skill\n" {
		t.Fatalf("user edit was clobbered: %q", got)
	}
}

func TestSeed_RefreshesUneditedStale(t *testing.T) {
	dir := t.TempDir()
	if _, err := Seed(dir, nil); err != nil {
		t.Fatal(err)
	}

	// Simulate a stale-but-unedited install: overwrite the on-disk file AND
	// the manifest hash to match it, so Seed believes the user hasn't touched
	// it and a newer embedded version exists.
	target := filepath.Join(dir, "calendar", "SKILL.md")
	stale := []byte("stale builtin content\n")
	if err := os.WriteFile(target, stale, 0o600); err != nil {
		t.Fatal(err)
	}
	m := loadManifest(dir)
	m["calendar"] = hashBytes(stale)
	if err := saveManifest(dir, m); err != nil {
		t.Fatal(err)
	}

	res, err := Seed(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(res.Updated, "calendar") {
		t.Fatalf("unedited stale skill should be updated, got %+v", res)
	}
	got, _ := os.ReadFile(target)
	if string(got) == string(stale) {
		t.Fatal("stale content was not refreshed")
	}
}

func keys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
