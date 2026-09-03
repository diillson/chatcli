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
)

func TestParseManaged(t *testing.T) {
	entries := ParseManaged(`
# org policy
CHATCLI_ENV_REDACT_MODE=strict
!CHATCLI_AUDIT_LOG_PATH="/var/log/chatcli/audit.jsonl"
export CHATCLI_LANG='pt-BR'
BAD KEY=1
=novalue
CHATCLI_X=value # trailing comment
`)
	if len(entries) != 4 {
		t.Fatalf("entries = %+v", entries)
	}
	if entries[0].Key != "CHATCLI_ENV_REDACT_MODE" || entries[0].Locked || entries[0].Value != "strict" {
		t.Fatalf("default entry: %+v", entries[0])
	}
	if !entries[1].Locked || entries[1].Value != "/var/log/chatcli/audit.jsonl" {
		t.Fatalf("locked quoted entry: %+v", entries[1])
	}
	if entries[2].Key != "CHATCLI_LANG" || entries[2].Value != "pt-BR" {
		t.Fatalf("export + single quotes: %+v", entries[2])
	}
	if entries[3].Value != "value" {
		t.Fatalf("trailing comment must be stripped: %+v", entries[3])
	}
}

func TestApplyManaged_PrecedenceAndReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "managed.env")
	if err := os.WriteFile(path, []byte("CHATCLI_TEST_MANAGED_DEFAULT=from-file\n!CHATCLI_TEST_MANAGED_LOCKED=policy\nCHATCLI_TEST_MANAGED_USER=ignored\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(ManagedConfigEnv, path)
	t.Setenv("CHATCLI_TEST_MANAGED_USER", "mine")
	t.Setenv("CHATCLI_TEST_MANAGED_LOCKED", "user-tried")
	t.Setenv("CHATCLI_TEST_MANAGED_DEFAULT", "")
	_ = os.Unsetenv("CHATCLI_TEST_MANAGED_DEFAULT")

	rep := ApplyManaged()
	if rep.Err != nil || !rep.Present || rep.Path != path {
		t.Fatalf("report = %+v", rep)
	}
	if os.Getenv("CHATCLI_TEST_MANAGED_DEFAULT") != "from-file" {
		t.Fatal("a managed default must fill an unset variable")
	}
	if os.Getenv("CHATCLI_TEST_MANAGED_USER") != "mine" {
		t.Fatal("a managed default must not override the user's value")
	}
	if os.Getenv("CHATCLI_TEST_MANAGED_LOCKED") != "policy" {
		t.Fatal("a locked entry must win over the user's value")
	}
	if len(rep.Applied) != 1 || len(rep.Locked) != 1 || len(rep.Skipped) != 1 {
		t.Fatalf("report buckets: applied=%d locked=%d skipped=%d", len(rep.Applied), len(rep.Locked), len(rep.Skipped))
	}
	// Simulated reload: the user flips the locked value; re-apply restores it.
	t.Setenv("CHATCLI_TEST_MANAGED_LOCKED", "flipped")
	ApplyManaged()
	if os.Getenv("CHATCLI_TEST_MANAGED_LOCKED") != "policy" {
		t.Fatal("reload must re-assert locked policies")
	}
	e, managed := ManagedEntryFor("CHATCLI_TEST_MANAGED_LOCKED")
	if !managed || !e.Locked {
		t.Fatal("ManagedEntryFor must report the locked entry")
	}
	if _, managed := ManagedEntryFor("CHATCLI_NOT_IN_FILE"); managed {
		t.Fatal("unknown keys are not managed")
	}
	p, present, entries := ManagedState()
	if p != path || !present || len(entries) != 3 || !entries[0].Locked {
		t.Fatalf("state = %s %v %+v", p, present, entries)
	}
}

func TestApplyManaged_MissingFileIsNotAnError(t *testing.T) {
	t.Setenv(ManagedConfigEnv, filepath.Join(t.TempDir(), "absent.env"))
	rep := ApplyManaged()
	if rep.Err != nil || rep.Present {
		t.Fatalf("missing file: %+v", rep)
	}
	if _, present, _ := ManagedState(); present {
		t.Fatal("state must report absence")
	}
}

func TestManagedConfigPaths_EnvAddsASecondFileNeverReplacesTheSystemOne(t *testing.T) {
	t.Setenv(ManagedConfigEnv, "/tmp/x.env")
	paths := ManagedConfigPaths()
	if len(paths) != 2 || paths[0] != ManagedConfigPath() || paths[1] != "/tmp/x.env" {
		t.Fatalf("paths = %v", paths)
	}
	t.Setenv(ManagedConfigEnv, "")
	if p := ManagedConfigPaths(); len(p) != 1 || p[0] == "" {
		t.Fatalf("a platform default must exist: %v", p)
	}
}

func TestApplyManaged_SystemLockedKeysCannotBeOverriddenByTheEnvFile(t *testing.T) {
	// The system file is a fixed path we cannot write in a test, so the
	// merge logic is exercised directly: entries from a later file never
	// redefine a key the first (system) file locked.
	dir := t.TempDir()
	sys := filepath.Join(dir, "system.env")
	extra := filepath.Join(dir, "extra.env")
	if err := os.WriteFile(sys, []byte("!CHATCLI_TEST_POLICY=locked\nCHATCLI_TEST_DEF=sys\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(extra, []byte("CHATCLI_TEST_POLICY=hijack\nCHATCLI_TEST_DEF=extra\nCHATCLI_TEST_NEW=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	merged := mergeManagedFiles([]string{sys, extra})
	got := map[string]ManagedEntry{}
	for _, e := range merged {
		got[e.Key] = e
	}
	if got["CHATCLI_TEST_POLICY"].Value != "locked" || !got["CHATCLI_TEST_POLICY"].Locked {
		t.Fatalf("system lock must survive: %+v", got["CHATCLI_TEST_POLICY"])
	}
	if got["CHATCLI_TEST_DEF"].Value != "extra" || got["CHATCLI_TEST_NEW"].Value != "1" {
		t.Fatalf("unlocked defaults may be redefined and new keys added: %+v", got)
	}
}
