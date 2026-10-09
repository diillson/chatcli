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
	"testing"
	"time"

	"github.com/diillson/chatcli/pkg/evals"
)

// markerHook writes a file when it runs, so a test can tell whether it fired.
func markerHook(t *testing.T) (*Manager, string) {
	t.Helper()
	marker := filepath.Join(t.TempDir(), "fired")
	m := NewManager(testLogger())
	m.hooks = []HookConfig{{
		Name: "marker", Event: EventPostToolUse, Type: HookTypeCommand,
		Command: "touch '" + marker + "'", Timeout: 5000,
	}}
	return m, marker
}

func fired(marker string) bool {
	_, err := os.Stat(marker)
	return err == nil
}

func firePost(m *Manager) {
	m.Fire(context.Background(), HookEvent{Type: EventPostToolUse, Timestamp: time.Now(), ToolName: "@coder"})
}

func TestFire_RunsWhenNotSuppressed(t *testing.T) {
	t.Setenv(EnabledEnv, "")
	t.Setenv(evalRecordEnv, "")
	m, marker := markerHook(t)
	firePost(m)
	if !fired(marker) {
		t.Fatal("an enabled hook must fire")
	}
}

func TestFire_KillSwitch(t *testing.T) {
	t.Setenv(evalRecordEnv, "")
	for _, v := range []string{"false", "0", "off", "NO"} {
		t.Setenv(EnabledEnv, v)
		m, marker := markerHook(t)
		firePost(m)
		if fired(marker) {
			t.Errorf("%s=%s must stop every hook", EnabledEnv, v)
		}
		if off, why := Suppressed(); !off || why != "disabled" {
			t.Errorf("Suppressed() = %v %q", off, why)
		}
	}
}

// Inside a `chatcli eval` candidate no hook fires, even when the kill switch
// says they are on: a hook that runs an eval would otherwise recurse.
func TestFire_NeverInsideAnEvalRun(t *testing.T) {
	t.Setenv(EnabledEnv, "true")
	t.Setenv(evalRecordEnv, filepath.Join(t.TempDir(), "record.json"))
	m, marker := markerHook(t)
	firePost(m)
	if fired(marker) {
		t.Fatal("hooks must not fire inside an eval run")
	}
	if off, why := Suppressed(); !off || why != "eval" {
		t.Errorf("Suppressed() = %v %q", off, why)
	}
}

func TestEvalRecordEnvMatchesTheHarness(t *testing.T) {
	if evalRecordEnv != evals.RecordEnv {
		t.Fatalf("hooks guards %q but the eval harness sets %q", evalRecordEnv, evals.RecordEnv)
	}
}
