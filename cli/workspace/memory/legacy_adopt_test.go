/*
 * ChatCLI - Long-term memory: legacy directory adoption tests.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package memory

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/diillson/chatcli/pkg/atrest"
)

// legacyLayout builds the shared directory the Helm chart used to mount:
// memory stores next to session files.
func legacyLayout(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "sessions")
	files := map[string]string{
		// memory
		"MEMORY.md":                   "# Memory\n- likes Go\n",
		"MEMORY.md.bak":               "old\n",
		"memory_index.json":           `{"facts":[]}`,
		"memory_tombstones.json":      `{}`,
		"episodes.json":               `[]`,
		"user_profile.json":           `{}`,
		"topics.json":                 `{}`,
		"projects.json":               `{}`,
		"usage_stats.json":            `{}`,
		"graph.json":                  `{}`,
		"vector_index.json":           `{}`,
		"memory_archive.json":         `[]`,
		"compactor_state.json":        `{}`,
		"episodes.json.corrupt-17000": "garbage",
		"202609/20260901.md":          "daily note\n",
		"weekly/2026-W36.md":          "week\n",
		"monthly/2026-08.md":          "month\n",
		"pending/seg-1.json":          `{"seg":1}`,
		// not memory: sessions and their neighbors
		"my-session.json":        `{"name":"my-session"}`,
		"another.json":           `{"name":"another"}`,
		"memory_index.json.lock": "",
		"notes/readme.md":        "not a memory dir",
	}
	for rel, content := range files {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func listTree(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	sort.Strings(out)
	return out
}

func TestAdoptLegacyDir_CopiesMemoryFilesOnly(t *testing.T) {
	legacy := legacyLayout(t)
	before := listTree(t, legacy)
	mem := filepath.Join(t.TempDir(), "memory")

	rep := AdoptLegacyDir(legacy, mem, zap.NewNop())
	if !rep.Ran || rep.Pending {
		t.Fatalf("report = %+v", rep)
	}
	want := []string{
		".migrated-from-legacy",
		"202609/20260901.md",
		"MEMORY.md",
		"MEMORY.md.bak",
		"compactor_state.json",
		"episodes.json",
		"episodes.json.corrupt-17000",
		"graph.json",
		"memory_archive.json",
		"memory_index.json",
		"memory_tombstones.json",
		"monthly/2026-08.md",
		"pending/seg-1.json",
		"projects.json",
		"topics.json",
		"usage_stats.json",
		"user_profile.json",
		"vector_index.json",
		"weekly/2026-W36.md",
	}
	if got := listTree(t, mem); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("memory dir:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if len(rep.Copied) != len(want)-1 {
		t.Errorf("copied %d files, want %d: %v", len(rep.Copied), len(want)-1, rep.Copied)
	}
	b, _ := os.ReadFile(filepath.Join(mem, "MEMORY.md"))
	if string(b) != "# Memory\n- likes Go\n" {
		t.Errorf("MEMORY.md content = %q", b)
	}
	// The legacy directory is never modified.
	if after := listTree(t, legacy); strings.Join(after, "\n") != strings.Join(before, "\n") {
		t.Errorf("legacy dir changed:\n%v\nwas:\n%v", after, before)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(mem, "episodes.json"))
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Errorf("episodes.json perm = %v (%v), want 0600", info.Mode().Perm(), err)
		}
	}
}

func TestAdoptLegacyDir_RunsOnce(t *testing.T) {
	legacy := legacyLayout(t)
	mem := filepath.Join(t.TempDir(), "memory")
	AdoptLegacyDir(legacy, mem, zap.NewNop())

	// A new file in the legacy dir after the adoption is not picked up, and
	// a file memory rewrote is not replaced.
	if err := os.WriteFile(filepath.Join(legacy, "topics.json"), []byte(`{"late":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mem, "episodes.json"), []byte(`["new"]`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(mem, "graph.json")); err != nil {
		t.Fatal(err)
	}
	rep := AdoptLegacyDir(legacy, mem, zap.NewNop())
	if rep.Ran || len(rep.Copied) != 0 {
		t.Fatalf("second run = %+v, want a no-op", rep)
	}
	if _, err := os.Stat(filepath.Join(mem, "graph.json")); err == nil {
		t.Error("a removed file was copied again after the marker")
	}
	b, _ := os.ReadFile(filepath.Join(mem, "episodes.json"))
	if string(b) != `["new"]` {
		t.Errorf("episodes.json = %q, overwritten", b)
	}
}

func TestAdoptLegacyDir_MemoryDirInUseIsLeftAlone(t *testing.T) {
	legacy := legacyLayout(t)
	mem := filepath.Join(t.TempDir(), "memory")
	if err := os.MkdirAll(mem, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mem, "memory_index.json"), []byte(`{"mine":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	rep := AdoptLegacyDir(legacy, mem, zap.NewNop())
	if rep.Ran {
		t.Fatalf("adopted into a memory dir already in use: %+v", rep)
	}
	if got := listTree(t, mem); len(got) != 1 {
		t.Errorf("memory dir = %v, want only its own file", got)
	}
	b, _ := os.ReadFile(filepath.Join(mem, "memory_index.json"))
	if string(b) != `{"mine":true}` {
		t.Errorf("memory_index.json = %q, overwritten", b)
	}
}

func TestAdoptLegacyDir_NeverOverwritesWhileResuming(t *testing.T) {
	legacy := legacyLayout(t)
	mem := filepath.Join(t.TempDir(), "memory")
	if err := os.MkdirAll(mem, 0o750); err != nil {
		t.Fatal(err)
	}
	// An earlier start left the adoption pending and memory wrote a file.
	if err := os.WriteFile(filepath.Join(mem, legacyAdoptingMarker), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mem, "topics.json"), []byte(`{"fresh":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	rep := AdoptLegacyDir(legacy, mem, zap.NewNop())
	if !rep.Ran || rep.Pending {
		t.Fatalf("report = %+v", rep)
	}
	b, _ := os.ReadFile(filepath.Join(mem, "topics.json"))
	if string(b) != `{"fresh":true}` {
		t.Errorf("topics.json = %q, overwritten", b)
	}
	if _, err := os.Stat(filepath.Join(mem, "episodes.json")); err != nil {
		t.Error("the rest of the adoption did not resume")
	}
	if _, err := os.Stat(filepath.Join(mem, legacyAdoptingMarker)); err == nil {
		t.Error("in-progress marker left after completion")
	}
}

func TestAdoptLegacyDir_UnreadableFileSkipped(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("file permissions do not restrict this user")
	}
	legacy := legacyLayout(t)
	unreadable := filepath.Join(legacy, "projects.json")
	if err := os.Chmod(unreadable, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(unreadable, 0o600) })
	mem := filepath.Join(t.TempDir(), "memory")

	rep := AdoptLegacyDir(legacy, mem, zap.NewNop())
	if !rep.Ran || rep.Pending {
		t.Fatalf("report = %+v", rep)
	}
	if _, err := os.Stat(filepath.Join(mem, "projects.json")); err == nil {
		t.Error("unreadable file copied")
	}
	if _, err := os.Stat(filepath.Join(mem, "episodes.json")); err != nil {
		t.Error("the other files were not copied")
	}
	found := false
	for _, s := range rep.Skipped {
		if strings.HasPrefix(s, "projects.json") {
			found = true
		}
	}
	if !found {
		t.Errorf("skipped = %v, want projects.json", rep.Skipped)
	}
	if _, err := os.Stat(filepath.Join(mem, legacyAdoptedMarker)); err != nil {
		t.Error("an unreadable file must not keep the adoption pending forever")
	}
}

func TestAdoptLegacyDir_NoOps(t *testing.T) {
	mem := filepath.Join(t.TempDir(), "memory")
	for name, legacy := range map[string]string{
		"unset":   "",
		"missing": filepath.Join(t.TempDir(), "nope"),
		"same":    mem,
	} {
		if rep := AdoptLegacyDir(legacy, mem, zap.NewNop()); rep.Ran {
			t.Errorf("%s: ran %+v", name, rep)
		}
	}
	if _, err := os.Stat(mem); err == nil {
		t.Error("memory dir created by a no-op")
	}
}

// A sealed store is bound to its path: it is resealed for the memory dir so
// the store opens there, and stays pending while the key is missing.
func TestAdoptLegacyDir_SealedFilesResealedForTheNewPath(t *testing.T) {
	t.Setenv(atrest.EnvKey, "test-key-for-legacy-adoption")
	legacy := filepath.Join(t.TempDir(), ".chatcli", "sessions")
	if err := os.MkdirAll(legacy, 0o750); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(legacy, "memory_index.json")
	sealed, err := atrest.SealAt(src, []byte(`{"facts":["sealed"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, sealed, 0o600); err != nil {
		t.Fatal(err)
	}
	mem := filepath.Join(filepath.Dir(legacy), "memory")
	dst := filepath.Join(mem, "memory_index.json")
	if _, err := atrest.OpenAt(dst, sealed); err == nil {
		t.Fatal("precondition: a path-bound payload must not open at another path")
	}

	// Key missing: nothing half-done, adoption pending.
	t.Setenv(atrest.EnvKey, "")
	rep := AdoptLegacyDir(legacy, mem, zap.NewNop())
	if !rep.Pending {
		t.Fatalf("report without the key = %+v, want pending", rep)
	}
	if _, err := os.Stat(dst); err == nil {
		t.Fatal("sealed file copied without being resealed")
	}

	// Key back: the next start finishes it.
	t.Setenv(atrest.EnvKey, "test-key-for-legacy-adoption")
	rep = AdoptLegacyDir(legacy, mem, zap.NewNop())
	if !rep.Ran || rep.Pending {
		t.Fatalf("report with the key = %+v", rep)
	}
	data, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := atrest.OpenAt(dst, data)
	if err != nil || string(plain) != `{"facts":["sealed"]}` {
		t.Fatalf("adopted sealed file opens to %q, %v", plain, err)
	}
}

// Round trip through the real stores: whatever a Manager persists in the
// shared directory is what the adoption carries over, so a store that adds
// a file without listing it in legacyStoreFiles fails here.
func TestAdoptLegacyDir_RealStoresRoundTrip(t *testing.T) {
	legacy := filepath.Join(t.TempDir(), "sessions")
	old := NewManager(legacy, DefaultConfig(), zap.NewNop())
	if !old.Facts.AddFact("the user deploys with Helm", "general", []string{"helm"}) {
		t.Fatal("fact not added")
	}
	old.Episodes.Add(Episode{ID: "ep-1", Summary: "migrated the chart", Date: time.Now()})
	if err := os.WriteFile(filepath.Join(legacy, "my-session.json"), []byte(`{"name":"my-session"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	mem := filepath.Join(t.TempDir(), "memory")
	rep := AdoptLegacyDir(legacy, mem, zap.NewNop())
	if !rep.Ran || rep.Pending {
		t.Fatalf("report = %+v", rep)
	}
	for _, f := range listTree(t, legacy) {
		if f == "my-session.json" || strings.HasSuffix(f, ".lock") {
			continue
		}
		if _, err := os.Stat(filepath.Join(mem, f)); err != nil {
			t.Errorf("store file %s was not adopted", f)
		}
	}
	if _, err := os.Stat(filepath.Join(mem, "my-session.json")); err == nil {
		t.Error("session file copied into memory")
	}
	fresh := NewManager(mem, DefaultConfig(), zap.NewNop())
	if fresh.Facts.Count() != 1 || fresh.Episodes.Count() != 1 {
		t.Errorf("adopted memory: %d facts, %d episodes; want 1 and 1", fresh.Facts.Count(), fresh.Episodes.Count())
	}
}
