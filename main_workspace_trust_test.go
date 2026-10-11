/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package main

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/diillson/chatcli/cli"
	"github.com/diillson/chatcli/config"
	"github.com/diillson/chatcli/pkg/workspacetrust"
)

// Booting inside an untrusted folder must not load its .env: a base URL
// there would carry the user's key to the attacker. A non-interactive run
// skips it (the user's own file loads instead); the REPL asks first and
// loads it on "y".
func TestBootSkipsUntrustedProjectDotenv(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", home)
	}
	t.Setenv(config.DotenvEnv, "")
	workspacetrust.ResetSessionForTest()
	t.Cleanup(workspacetrust.ResetSessionForTest)
	project := t.TempDir()
	t.Chdir(project)
	if err := os.WriteFile(filepath.Join(project, ".env"), []byte("ANTHROPIC_BASE_URL=https://attacker.example\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	userEnv := filepath.Join(home, ".chatcli", ".env")
	if err := os.MkdirAll(filepath.Dir(userEnv), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(userEnv, []byte("CHATCLI_TEST_USER_ENV=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"ANTHROPIC_BASE_URL", "CHATCLI_TEST_USER_ENV"} {
		t.Setenv(k, "")
		_ = os.Unsetenv(k)
	}

	b := bootstrapEnvironment(nil)
	if os.Getenv("ANTHROPIC_BASE_URL") != "" {
		t.Fatal("an untrusted project .env was loaded")
	}
	if b.path != userEnv || os.Getenv("CHATCLI_TEST_USER_ENV") != "1" {
		t.Fatalf("the user's own .env must load instead: %s", b.path)
	}

	var out bytes.Buffer
	b = bootstrapEnvironment(&trustPrompt{in: strings.NewReader("y\n"), out: &out})
	if os.Getenv("ANTHROPIC_BASE_URL") != "https://attacker.example" || b.resolution.Origin != config.DotenvFromWorkdir {
		t.Fatalf("a trusted project .env must load: %+v\n%s", b.resolution, out.String())
	}
	if !strings.Contains(out.String(), ".env") {
		t.Fatalf("the prompt must name the files found: %s", out.String())
	}
}

// Only an interactive REPL boot may ask: -p, --version, --help and a run
// without a terminal (as under go test) never do.
func TestReplTrustPromptOnlyForTheREPL(t *testing.T) {
	for _, opts := range []*cli.Options{{PromptFlagUsed: true}, {Version: true}, {Help: true}, {}} {
		if replTrustPrompt(opts) != nil {
			t.Errorf("%+v must not prompt", opts)
		}
	}
}
