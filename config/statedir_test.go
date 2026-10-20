/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/joho/godotenv"
)

func TestKeepStateDir(t *testing.T) {
	envFile := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(envFile, []byte("CHATCLI_STATE_DIR=/from/file\nCHATCLI_LANG=en\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Run("a file cannot set it", func(t *testing.T) {
		t.Setenv("CHATCLI_STATE_DIR", "")
		_ = os.Unsetenv("CHATCLI_STATE_DIR")
		keep := KeepStateDir()
		if err := godotenv.Overload(envFile); err != nil {
			t.Fatal(err)
		}
		if ignored := keep(); ignored != "/from/file" {
			t.Errorf("ignored = %q, want the file's value", ignored)
		}
		if v, set := os.LookupEnv("CHATCLI_STATE_DIR"); set {
			t.Errorf("the variable must be unset again, got %q", v)
		}
	})

	t.Run("a file cannot change it", func(t *testing.T) {
		t.Setenv("CHATCLI_STATE_DIR", "/from/process")
		keep := KeepStateDir()
		if err := godotenv.Overload(envFile); err != nil {
			t.Fatal(err)
		}
		if ignored := keep(); ignored != "/from/file" {
			t.Errorf("ignored = %q", ignored)
		}
		if got := os.Getenv("CHATCLI_STATE_DIR"); got != "/from/process" {
			t.Errorf("the process value must win, got %q", got)
		}
	})

	t.Run("an untouched variable reports nothing", func(t *testing.T) {
		t.Setenv("CHATCLI_STATE_DIR", "/from/process")
		keep := KeepStateDir()
		if ignored := keep(); ignored != "" {
			t.Errorf("ignored = %q, want empty", ignored)
		}
	})
}
