/*
 * ChatCLI - Shared self-provisioning helper tests.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package provision

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// findPlanted walks base and reports any file named "evil" — the marker
// the escape payloads try to plant outside the extraction dir.
func findPlanted(base string) []string {
	var hits []string
	_ = filepath.WalkDir(base, func(p string, d os.DirEntry, err error) error {
		if err == nil && d.Name() == "evil" {
			hits = append(hits, p)
		}
		return nil
	})
	return hits
}

// TestExtractTar_SymlinkChainCannotEscape is the regression for the proven
// payload: every link passes a lexical check, yet the chain makes t point
// two levels above destDir and the next entry writes t/evil through it.
func TestExtractTar_SymlinkChainCannotEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on windows")
	}
	base := t.TempDir()
	dest := filepath.Join(base, "x", "y", "dest")
	data := buildTar(t, []tarEntry{
		{name: "s", link: "."},
		{name: "s/s2", link: "."},
		{name: "s/s2/t", link: "../.."},
		{name: "t/evil", body: "planted"},
	})
	if err := extractTar(bytes.NewReader(data), dest); err == nil {
		t.Fatal("the symlink-chain payload must be rejected")
	}
	if hits := findPlanted(base); len(hits) != 0 {
		t.Fatalf("payload wrote outside destDir: %v", hits)
	}
}

// Without a follow-up write, an archive still must not leave behind a link
// that resolves outside destDir (or nowhere).
func TestExtractTar_RejectsLinkThatEscapesOnlyWhenResolved(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on windows")
	}
	for name, entries := range map[string][]tarEntry{
		"chain": {
			{name: "s", link: "."},
			{name: "s/s2", link: "."},
			{name: "s/s2/t", link: "../.."},
		},
		"dotdot through link": {
			{name: "s", link: "."},
			{name: "z", link: "s/.."},
		},
		"dangling": {
			{name: "lib/libalias.so", link: "libmissing.so"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := extractTar(bytes.NewReader(buildTar(t, entries)), t.TempDir()); err == nil {
				t.Fatal("a link that does not resolve inside destDir must be rejected")
			}
		})
	}
}

func TestExtractTar_HardLinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("hard links and symlinks need privileges on windows")
	}
	dest := t.TempDir()
	data := buildTar(t, []tarEntry{
		{name: "lib/libreal.so", body: "lib"},
		{name: "lib/libcopy.so", hard: "lib/libreal.so"},
	})
	if err := extractTar(bytes.NewReader(data), dest); err != nil {
		t.Fatalf("in-tree hard link: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(dest, "lib", "libcopy.so")); err != nil || string(got) != "lib" {
		t.Fatalf("hard link content = %q, %v", got, err)
	}

	base := t.TempDir()
	outside := filepath.Join(base, "outside.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, entries := range map[string][]tarEntry{
		"escaping target":    {{name: "grab", hard: "../outside.txt"}},
		"absolute target":    {{name: "grab", hard: outside}},
		"through a symlink":  {{name: "up", link: "."}, {name: "grab", hard: "up/../outside.txt"}},
		"to a symlink entry": {{name: "lib/real", body: "x"}, {name: "lib/alias", link: "real"}, {name: "grab", hard: "lib/alias"}},
	} {
		t.Run(name, func(t *testing.T) {
			dest := filepath.Join(base, "dest-"+strings.ReplaceAll(name, " ", "-"))
			if err := extractTar(bytes.NewReader(buildTar(t, entries)), dest); err == nil {
				t.Fatal("hard link must be refused")
			}
			if _, err := os.Lstat(filepath.Join(dest, "grab")); err == nil {
				t.Fatal("no hard link may be created")
			}
		})
	}
}

func TestExtractTar_StripsSpecialModeBits(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX mode bits")
	}
	dest := t.TempDir()
	data := buildTar(t, []tarEntry{{name: "bin/suid", body: "x", mode: 0o6777}})
	if err := extractTar(bytes.NewReader(data), dest); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(dest, "bin", "suid"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&(os.ModeSetuid|os.ModeSetgid) != 0 || fi.Mode().Perm()&0o022 != 0 {
		t.Fatalf("special or group/world-writable bits survived: %v", fi.Mode())
	}
}
