/*
 * ChatCLI - Per-conversation voice preference tests.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package gateway

import (
	"os"
	"path/filepath"
	"testing"
)

func TestVoicePrefs_SetGetAndDelete(t *testing.T) {
	v := NewVoicePrefs("")
	if got := v.Get("telegram:1"); got != "" {
		t.Fatalf("unset session = %q, want empty", got)
	}
	if err := v.Set("telegram:1", VoicePrefAlways); err != nil {
		t.Fatal(err)
	}
	if got := v.Get("telegram:1"); got != VoicePrefAlways {
		t.Fatalf("Get = %q, want always", got)
	}
	if err := v.Set("telegram:1", ""); err != nil {
		t.Fatal(err)
	}
	if got := v.Get("telegram:1"); got != "" {
		t.Fatalf("deleted session = %q, want empty", got)
	}
}

func TestVoicePrefs_RejectsInvalid(t *testing.T) {
	v := NewVoicePrefs("")
	if err := v.Set("", VoicePrefAlways); err == nil {
		t.Fatal("empty session must be rejected")
	}
	if err := v.Set("telegram:1", "loud"); err == nil {
		t.Fatal("invalid mode must be rejected")
	}
}

func TestVoicePrefs_PersistsAcrossReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prefs.json")
	v := NewVoicePrefs(path)
	if err := v.Set("telegram:7", VoicePrefNever); err != nil {
		t.Fatal(err)
	}
	if err := v.Set("discord:9", VoicePrefAlways); err != nil {
		t.Fatal(err)
	}

	reloaded := NewVoicePrefs(path)
	if got := reloaded.Get("telegram:7"); got != VoicePrefNever {
		t.Fatalf("reloaded telegram:7 = %q, want never", got)
	}
	if got := reloaded.Get("discord:9"); got != VoicePrefAlways {
		t.Fatalf("reloaded discord:9 = %q, want always", got)
	}
}

func TestVoicePrefs_CorruptFileStartsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prefs.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	v := NewVoicePrefs(path)
	if got := v.Get("telegram:1"); got != "" {
		t.Fatalf("corrupt file must start empty, got %q", got)
	}
}

func TestVoicePrefs_ActiveSession(t *testing.T) {
	v := NewVoicePrefs("")
	if got := v.ActiveSession(); got != "" {
		t.Fatalf("initial active = %q, want empty", got)
	}
	v.SetActiveSession("telegram:5")
	if got := v.ActiveSession(); got != "telegram:5" {
		t.Fatalf("active = %q, want telegram:5", got)
	}
	v.SetActiveSession("")
	if got := v.ActiveSession(); got != "" {
		t.Fatalf("cleared active = %q, want empty", got)
	}
}
