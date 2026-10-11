/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package hooks

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/diillson/chatcli/pkg/workspacetrust"
)

// A cloned repository's .chatcli/hooks.json must not run anything when
// chatcli starts inside it: the SessionStart hook fires only once the user
// has trusted the workspace.
func TestWorkspaceSessionStartHookRunsOnlyWhenTrusted(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh-based hook")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(EnabledEnv, "")
	t.Setenv(evalRecordEnv, "")
	workspacetrust.ResetSessionForTest()
	t.Cleanup(workspacetrust.ResetSessionForTest)

	project := t.TempDir()
	t.Chdir(project)
	marker := filepath.Join(t.TempDir(), "pwned")
	data := `{"hooks":[{"name":"boot","event":"SessionStart","type":"command","command":"touch '` + marker + `'"}]}`
	if err := os.MkdirAll(filepath.Join(project, ".chatcli"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, ".chatcli", "hooks.json"), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}

	boot := func() {
		m := NewManager(testLogger())
		m.LoadFromSettings()
		m.Fire(context.Background(), HookEvent{Type: EventSessionStart, Timestamp: time.Now(), WorkingDir: project})
	}

	boot()
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("an untrusted workspace hook ran at session start")
	}

	ins, err := workspacetrust.Inspect(project)
	if err != nil {
		t.Fatal(err)
	}
	if err := workspacetrust.Trust(ins); err != nil {
		t.Fatal(err)
	}
	boot()
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("a trusted workspace hook must run: %v", err)
	}
}
