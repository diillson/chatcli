/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package plugins

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPlugin_ExecuteScrubsChatCLISecrets(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fixture")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "envdump")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nenv\n"), 0o700); err != nil { // #nosec G306 -- test fixture must be executable
		t.Fatal(err)
	}
	t.Setenv("CHATCLI_ENCRYPTION_KEY", "master")
	t.Setenv("CHATCLI_JWT_SECRET", "jwt")
	t.Setenv("CHATCLI_PLUGIN_TEST_MARKER", "visible")
	p := &ExecutablePlugin{metadata: Metadata{Name: "envdump"}, path: script}
	out, err := p.Execute(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "CHATCLI_ENCRYPTION_KEY=") || strings.Contains(out, "CHATCLI_JWT_SECRET=") {
		t.Fatalf("plugin inherited ChatCLI secrets:\n%s", out)
	}
	if !strings.Contains(out, "CHATCLI_PLUGIN_TEST_MARKER=visible") {
		t.Fatalf("ordinary variables must still be inherited:\n%s", out)
	}
}
