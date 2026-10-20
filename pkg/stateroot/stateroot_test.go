/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package stateroot

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDirDefaultsToHomeDotChatcli(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv(Env, "")
	got, err := Dir()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, DefaultName); got != want {
		t.Errorf("Dir() = %q, want %q", got, want)
	}
	if Custom() {
		t.Error("an unset variable is not a custom root")
	}
	if got := Under(home); got != filepath.Join(home, DefaultName) {
		t.Errorf("Under(home) = %q", got)
	}
}

func TestDirFollowsTheVariable(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	abs := filepath.Join(t.TempDir(), "state")
	t.Setenv(Env, "  "+abs+"  ")
	if got, err := Dir(); err != nil || got != abs {
		t.Errorf("absolute: Dir() = %q, %v; want %q", got, err, abs)
	}
	if !Custom() {
		t.Error("a set variable is a custom root")
	}
	// Under ignores the caller's home once the variable is set.
	if got := Under("/elsewhere"); got != abs {
		t.Errorf("Under ignores home when set: got %q", got)
	}

	t.Setenv(Env, "~/bench/state")
	if got, _ := Dir(); got != filepath.Join(home, "bench", "state") {
		t.Errorf("tilde: Dir() = %q", got)
	}

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(Env, "rel-state")
	if got, _ := Dir(); got != filepath.Join(cwd, "rel-state") {
		t.Errorf("relative: Dir() = %q, want it absolute under %q", got, cwd)
	}
}
