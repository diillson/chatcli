/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package testenv

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsolate_RedirectsHomeAndPinsTheFileKeychain(t *testing.T) {
	realHome, _ := os.UserHomeDir()
	t.Setenv("HOME", realHome) // restored by the test framework afterwards
	t.Setenv(KeychainBackendEnv, "")

	home, cleanup := Isolate()
	if home == "" {
		t.Fatal("no isolated home")
	}
	got, err := os.UserHomeDir()
	if err != nil || got != home {
		t.Fatalf("UserHomeDir = %q, %v; want the isolated %q", got, err, home)
	}
	if strings.HasPrefix(home, realHome+string(filepath.Separator)+".chatcli") {
		t.Fatalf("isolated home %q is inside the real store", home)
	}
	if v := os.Getenv(KeychainBackendEnv); v != "file" {
		t.Fatalf("%s = %q, want file", KeychainBackendEnv, v)
	}
	if err := os.WriteFile(filepath.Join(home, "probe"), []byte("x"), 0o600); err != nil {
		t.Fatalf("isolated home is not writable: %v", err)
	}
	cleanup()
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatalf("cleanup left %q behind (%v)", home, err)
	}
}

func TestIsolate_DropsStateAndCredentialRelocation(t *testing.T) {
	realHome, _ := os.UserHomeDir()
	t.Setenv("HOME", realHome)
	t.Setenv("CHATCLI_STATE_DIR", filepath.Join(realHome, "some-real-state"))
	t.Setenv("CHATCLI_AUTH_DIR", filepath.Join(realHome, ".chatcli"))

	_, cleanup := Isolate()
	defer cleanup()
	for _, name := range []string{"CHATCLI_STATE_DIR", "CHATCLI_AUTH_DIR"} {
		if v, set := os.LookupEnv(name); set {
			t.Errorf("%s = %q survived isolation; the suite would write to a real store", name, v)
		}
	}
}
