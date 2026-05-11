/*
 * ChatCLI - Tests for SkillHandler.Info
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Info is the most metadata-shaped of the SkillHandler commands: each row
 * comes from a tiny helper (printSkillInfoHeader, printSkillInfoDetails,
 * printSkillInstallStatus, …). We drive them through the public Info
 * entrypoint with a real install directory so the printSkillInstallStatus
 * "found local" and "not found anywhere" branches both execute, and the
 * nil-safe accessors get fed both filled and empty inputs.
 */
package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/diillson/chatcli/pkg/persona"
	"github.com/diillson/chatcli/pkg/registry"
	"go.uber.org/zap"
)

// newInfoFixture builds a SkillHandler whose registry install dir is a
// temp directory we control. Returns (handler, installDir). Tests can
// pre-populate installDir with skill packages and then call sh.Info.
func newInfoFixture(t *testing.T) (*SkillHandler, string) {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("CHATCLI_SKILL_INSTALL_DIR", tmp)
	mgr := persona.NewManager(zap.NewNop())
	sh := NewSkillHandler(zap.NewNop(), mgr)
	if sh.registryMgr == nil {
		t.Fatal("registryMgr unexpectedly nil after NewSkillHandler")
	}
	if got := sh.registryMgr.GetInstallDir(); got != tmp {
		t.Fatalf("registry install dir = %q, want %q (env override not honored)", got, tmp)
	}
	return sh, tmp
}

func writeInstalledSkill(t *testing.T, installDir, qualifiedName, body string) {
	t.Helper()
	dir := filepath.Join(installDir, qualifiedName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	skillFile := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(skillFile, []byte(body), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
}

func TestSkillHandlerInfo_NotFoundEverywhere(t *testing.T) {
	sh, _ := newInfoFixture(t)
	// Both local install dir AND remote registries are empty/disabled —
	// Info must reach the "not found" message and return without panic.
	sh.Info("does-not-exist", "")
}

func TestSkillHandlerInfo_LocalOnlyPrintsStatus(t *testing.T) {
	sh, installDir := newInfoFixture(t)
	writeInstalledSkill(t, installDir, "local--ordinary-skill", `---
name: ordinary-skill
description: a local-only skill
version: 1.0
---
body
`)
	// Sanity: the registry now reports it.
	if !sh.registryMgr.IsInstalled("local--ordinary-skill") {
		t.Fatal("fixture broken: registry did not register the installed skill")
	}

	// Info itself prints to stdout; we cannot easily capture without
	// extra plumbing. Suppress the output and assert no panic — the
	// branches under test (printSkillInfoHeader, printSkillInfoDetails,
	// printSkillInstallStatus single-install) all execute.
	withSilencedStdout(t, func() {
		sh.Info("ordinary-skill", "")
	})
}

func TestSkillHandlerInfo_MultipleLocalInstallsRendersConflictView(t *testing.T) {
	sh, installDir := newInfoFixture(t)
	// Two installs of the "echo" base name from different sources.
	writeInstalledSkill(t, installDir, "skills.sh--echo", `---
name: echo
description: skills.sh variant
version: 1.0
---
body
`)
	writeInstalledSkill(t, installDir, "local--echo", `---
name: echo
description: local variant
version: 2.0
---
body
`)
	// We assert through the registry API rather than stdout: the helper
	// that powers the "multiple sources" branch is GetAllInstalledInfo.
	matches := sh.registryMgr.GetAllInstalledInfo("echo")
	if len(matches) < 2 {
		t.Fatalf("expected at least 2 installs; got %d", len(matches))
	}
	// Info itself just needs to not crash on the multi-install path.
	withSilencedStdout(t, func() {
		sh.Info("echo", "")
	})
}

func TestSelectRichestRemote_PrefersBetterDescription(t *testing.T) {
	// This case is reachable through Info → resolveRemoteSkillMeta. The
	// pure helper handles it cleanly when called directly.
	candidates := []*registry.SkillMeta{
		{RegistryName: "first", Downloads: 0, Description: ""},
		{RegistryName: "second", Downloads: 0, Description: "non-empty"},
	}
	got := selectRichestRemote(candidates)
	if got == nil || got.RegistryName != "second" {
		t.Errorf("expected description-bearing entry to win; got %+v", got)
	}
}

// withSilencedStdout redirects os.Stdout to /dev/null for the duration of
// fn. We use it for Info tests where stdout would otherwise clutter the
// test runner — the assertion under test is "no panic / no nil-deref",
// not the textual output.
func withSilencedStdout(t *testing.T, fn func()) {
	t.Helper()
	old := os.Stdout
	devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		// Not fatal — best effort. Tests still run, just noisier.
		fn()
		return
	}
	os.Stdout = devnull
	defer func() {
		os.Stdout = old
		_ = devnull.Close()
	}()
	fn()
}
