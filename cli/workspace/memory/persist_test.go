package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/zap"
)

// TestFactIndexCorruptFileIsQuarantinedNotClobbered pins the recovery contract
// for the worst failure mode a memory system can have: silent, permanent loss.
// A corrupted memory_index.json (crash mid-write, disk hiccup) must be moved
// aside for recovery — NOT left in place for the next persist to overwrite
// with the empty post-corruption state, which is how months of accumulated
// memory used to vanish without a single visible error.
func TestFactIndexCorruptFileIsQuarantinedNotClobbered(t *testing.T) {
	dir := t.TempDir()
	logger := zap.NewNop()

	fi := NewFactIndex(dir, DefaultConfig(), logger)
	if !fi.AddFact("user prefers tabs over spaces", "preference", nil) {
		t.Fatal("seed fact not added")
	}
	path := filepath.Join(dir, "memory_index.json")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("index file missing after persist: %v", err)
	}

	// Corrupt the file — the shape a crash mid-write leaves behind.
	corrupt := []byte(`[{"id":"abc","content":"user prefers tabs ov`)
	if err := os.WriteFile(path, corrupt, 0o600); err != nil {
		t.Fatal(err)
	}

	// Restart: the new index cannot parse the file (starting empty is
	// acceptable) but the corrupt bytes MUST survive somewhere recoverable.
	fi2 := NewFactIndex(dir, DefaultConfig(), logger)
	if fi2.Count() != 0 {
		t.Fatalf("expected empty index after corruption, got %d facts", fi2.Count())
	}
	fi2.AddFact("a brand new fact after the incident", "general", nil) // triggers persist

	// The live file now holds the new state...
	live, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(live), "tabs ov") {
		t.Fatal("live index still contains the corrupt payload")
	}

	// ...and the corrupt original must exist as a quarantine file so the user
	// (or a recovery tool) can still extract the pre-crash facts.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range entries {
		if strings.Contains(e.Name(), "memory_index.json.corrupt") {
			data, rerr := os.ReadFile(filepath.Join(dir, e.Name()))
			if rerr != nil {
				t.Fatal(rerr)
			}
			if string(data) == string(corrupt) {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("corrupt index was overwritten with no quarantine copy — accumulated memory unrecoverable")
	}
}

// TestAtomicWriteFileMechanics pins the write helper: content lands intact,
// no temp litter remains, and an existing file is replaced atomically.
func TestAtomicWriteFileMechanics(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "store.json")

	if err := atomicWriteFile(path, []byte(`{"v":1}`), 0o600); err != nil {
		t.Fatalf("atomicWriteFile: %v", err)
	}
	if err := atomicWriteFile(path, []byte(`{"v":2}`), 0o600); err != nil {
		t.Fatalf("atomicWriteFile overwrite: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"v":2}` {
		t.Fatalf("content = %s", data)
	}

	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp") {
			t.Fatalf("temp file litter left behind: %s", e.Name())
		}
	}
}

// TestProfileCorruptFileIsQuarantined extends the quarantine contract to the
// profile store — every memory substore shares the same failure mode.
func TestProfileCorruptFileIsQuarantined(t *testing.T) {
	dir := t.TempDir()
	logger := zap.NewNop()

	ps := NewUserProfileStore(dir, logger)
	ps.Update(map[string]string{"name": "Edilson"})
	path := filepath.Join(dir, "user_profile.json")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("profile file missing: %v", err)
	}

	if err := os.WriteFile(path, []byte(`{"name":"Edi`), 0o600); err != nil {
		t.Fatal(err)
	}

	_ = NewUserProfileStore(dir, logger)
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.Contains(e.Name(), "user_profile.json.corrupt") {
			return // quarantined — contract honored
		}
	}
	t.Fatal("corrupt user_profile.json not quarantined")
}
